package monitor

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/usage"
)

// fakeRefresher hands out queued states: the first Refresh yields the first
// state, the second the second, and the last one repeats.
type fakeRefresher struct {
	states   []*MonitorState
	err      error
	refreshs int
}

func (f *fakeRefresher) Refresh(ctx context.Context) error {
	f.refreshs++
	return f.err
}

func (f *fakeRefresher) GetState() *MonitorState {
	if len(f.states) == 0 {
		return &MonitorState{Profiles: map[string]*ProfileState{}}
	}
	i := f.refreshs - 1
	if i < 0 {
		i = 0
	}
	if i >= len(f.states) {
		i = len(f.states) - 1
	}
	return f.states[i]
}

func pdt() *time.Location { return time.FixedZone("PDT", -7*3600) }

// dashNow is 2026-09-13 13:40 PDT, the day the switcher was built.
func dashNow() time.Time { return time.Date(2026, 9, 13, 13, 40, 0, 0, pdt()) }

func stateWith(profiles ...*ProfileState) *MonitorState {
	s := &MonitorState{Profiles: map[string]*ProfileState{}, UpdatedAt: dashNow()}
	for _, p := range profiles {
		s.Profiles[profileKey(p.Provider, p.ProfileName)] = p
	}
	return s
}

func claudeProfile(name string, active bool) *ProfileState {
	now := dashNow()
	return &ProfileState{
		Provider: "claude", ProfileName: name, Active: active,
		Usage: &usage.UsageInfo{
			Provider: "claude", ProfileName: name, FetchedAt: now,
			PrimaryWindow:   &usage.UsageWindow{UsedPercent: 18, ResetsAt: now.Add(4*time.Hour + 30*time.Minute), WindowDuration: 5 * time.Hour, Kind: "session"},
			SecondaryWindow: &usage.UsageWindow{UsedPercent: 50, ResetsAt: time.Date(2026, 9, 15, 17, 0, 0, 0, pdt()), WindowDuration: 7 * 24 * time.Hour, Kind: "weekly_all"},
			ModelWindows: map[string]*usage.UsageWindow{
				"Fable": {UsedPercent: 90, ResetsAt: time.Date(2026, 9, 15, 17, 0, 0, 0, pdt()), WindowDuration: 7 * 24 * time.Hour, Kind: "weekly_scoped", Label: "Fable"},
			},
		},
	}
}

func codexProfile(name string, active bool) *ProfileState {
	return &ProfileState{
		Provider: "codex", ProfileName: name, Active: active,
		Usage: &usage.UsageInfo{
			Provider: "codex", ProfileName: name, FetchedAt: dashNow(),
			PrimaryWindow: &usage.UsageWindow{UsedPercent: 70, ResetsAt: time.Date(2026, 9, 20, 8, 45, 0, 0, pdt()), WindowDuration: 7 * 24 * time.Hour},
		},
	}
}

func newTestDashboard(states []*MonitorState, sw SwitchFunc) (*Dashboard, *fakeRefresher) {
	ref := &fakeRefresher{states: states}
	d := NewDashboard(ref, DashboardOptions{Interval: time.Minute, Switch: sw, Now: dashNow})
	d.width = 200
	return d, ref
}

// load runs one refresh round-trip through Update.
func load(t *testing.T, d *Dashboard) {
	t.Helper()
	cmd := d.refreshCmd()
	msg := cmd()
	if _, ok := msg.(refreshDoneMsg); !ok {
		t.Fatalf("refresh produced %T, want refreshDoneMsg", msg)
	}
	d.Update(msg)
}

func key(s string) tea.KeyMsg {
	switch s {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func TestDashboard_ColumnsAreTheUnionOfWindowsInOrder(t *testing.T) {
	d, _ := newTestDashboard([]*MonitorState{stateWith(claudeProfile("chris", true), codexProfile("ops", true))}, nil)
	load(t, d)

	got := strings.Join(d.Columns(), "|")
	if got != "5-HOUR|WEEKLY|WEEKLY FABLE" {
		t.Fatalf("columns = %q", got)
	}
}

// Every window is two columns: what is left under the window's name, and
// the local clock it resets at under a RESETS column beside it.
func TestDashboard_CellsShowPercentLeftAndLocalReset(t *testing.T) {
	d, _ := newTestDashboard([]*MonitorState{stateWith(claudeProfile("chris", true), codexProfile("ops", true))}, nil)
	load(t, d)
	view := d.View()

	wantRows := map[string][]string{
		"PROFILE": {"PROFILE", "5-HOUR", "RESETS", "WEEKLY", "RESETS", "WEEKLY FABLE", "RESETS", "STATUS"},
		// The 5-hour window resets today (clock only); Fable is the per-model
		// weekly; a reset a week out shows the date, not the weekday.
		"* claude/chris": {"* claude/chris", "82% left", "6:10 PM", "50% left", "Tue 5:00 PM", "10% left", "Tue 5:00 PM", "ok"},
		"* codex/ops":    {"* codex/ops", "-", "-", "30% left", "Sep 20 8:45 AM", "-", "-", "ok"},
	}
	found := 0
	for _, line := range strings.Split(view, "\n") {
		fields := tableFields(line)
		if len(fields) == 0 {
			continue
		}
		if want, ok := wantRows[fields[0]]; ok {
			found++
			if strings.Join(fields, "|") != strings.Join(want, "|") {
				t.Errorf("row %q fields = %q, want %q", fields[0], fields, want)
			}
		}
	}
	if found != len(wantRows) {
		t.Errorf("found %d of %d expected rows:\n%s", found, len(wantRows), view)
	}
	// No secrets, no "used" framing, no combined cell.
	for _, gone := range []string{"used", ", resets "} {
		if strings.Contains(view, gone) {
			t.Errorf("view still says %q:\n%s", gone, view)
		}
	}
}

// tableFields splits a padded table line on its two-space-or-wider gaps.
func tableFields(line string) []string {
	var fields []string
	for _, f := range regexp.MustCompile(`\s{2,}`).Split(strings.TrimSpace(line), -1) {
		if f != "" {
			fields = append(fields, f)
		}
	}
	return fields
}

func TestDashboard_EnterConfirmsThenSwitchesAndMovesTheStar(t *testing.T) {
	var calls []string
	sw := func(ctx context.Context, provider, profile string) (string, error) {
		calls = append(calls, provider+"/"+profile)
		return "Activated " + provider + " profile '" + profile + "'", nil
	}
	first := stateWith(codexProfile("a", false), codexProfile("b", true))
	after := stateWith(codexProfile("a", true), codexProfile("b", false))
	d, ref := newTestDashboard([]*MonitorState{first, after}, sw)
	load(t, d)

	// Cursor starts on codex/a (rows sort by name); Enter asks first.
	d.Update(key("enter"))
	if d.mode != modeConfirm || len(calls) != 0 {
		t.Fatalf("enter should confirm before switching: mode=%v calls=%v", d.mode, calls)
	}
	if v := d.View(); !strings.Contains(v, "Switch codex to a?") {
		t.Fatalf("confirmation prompt missing:\n%s", v)
	}

	// n cancels without a call.
	d.Update(key("n"))
	if d.mode != modeBrowse || len(calls) != 0 {
		t.Fatalf("n should cancel: mode=%v calls=%v", d.mode, calls)
	}

	// y runs the switch; input is ignored until it reports back.
	d.Update(key("enter"))
	_, cmd := d.Update(key("y"))
	if d.mode != modeSwitching || cmd == nil {
		t.Fatalf("y should start the switch: mode=%v cmd=%v", d.mode, cmd)
	}
	d.Update(key("enter"))
	if d.mode != modeSwitching {
		t.Fatalf("keys must be ignored while switching")
	}
	msg := cmd()
	done, ok := msg.(switchDoneMsg)
	if !ok || done.err != nil {
		t.Fatalf("switch cmd produced %#v", msg)
	}
	if len(calls) != 1 || calls[0] != "codex/a" {
		t.Fatalf("switcher calls = %v", calls)
	}

	_, cmd = d.Update(done)
	rows := d.Rows()
	if !rows[0].Active || rows[1].Active {
		t.Fatalf("star did not move to codex/a: %+v", rows)
	}
	if d.mode != modeBrowse || d.messageErr || !strings.Contains(d.message, "Activated") {
		t.Fatalf("after switch: mode=%v message=%q err=%v", d.mode, d.message, d.messageErr)
	}
	// A refresh follows to confirm from live state.
	if cmd == nil {
		t.Fatalf("no refresh scheduled after the switch")
	}
	d.Update(cmd())
	if ref.refreshs != 2 {
		t.Fatalf("refreshes = %d, want 2", ref.refreshs)
	}
	rows = d.Rows()
	if !rows[0].Active || rows[1].Active {
		t.Fatalf("refreshed state disagrees with the switch: %+v", rows)
	}
}

func TestDashboard_FailedSwitchKeepsTheStar(t *testing.T) {
	sw := func(ctx context.Context, provider, profile string) (string, error) {
		return "", errors.New("could not re-capture the outgoing profile b before switching")
	}
	d, _ := newTestDashboard([]*MonitorState{stateWith(codexProfile("a", false), codexProfile("b", true))}, sw)
	load(t, d)

	d.Update(key("enter"))
	_, cmd := d.Update(key("y"))
	d.Update(cmd())

	rows := d.Rows()
	if rows[0].Active || !rows[1].Active {
		t.Fatalf("star moved despite the failure: %+v", rows)
	}
	if !d.messageErr || !strings.Contains(d.message, "re-capture") {
		t.Fatalf("failure not surfaced: %q", d.message)
	}
	if d.mode != modeBrowse {
		t.Fatalf("mode after failure = %v", d.mode)
	}
}

func TestDashboard_EnterOnTheActiveRowDoesNothing(t *testing.T) {
	called := false
	sw := func(ctx context.Context, provider, profile string) (string, error) {
		called = true
		return "", nil
	}
	d, _ := newTestDashboard([]*MonitorState{stateWith(codexProfile("a", true))}, sw)
	load(t, d)

	_, cmd := d.Update(key("enter"))
	if d.mode != modeBrowse || cmd != nil || called {
		t.Fatalf("active row must not be switchable: mode=%v cmd=%v called=%v", d.mode, cmd, called)
	}
	if !strings.Contains(d.message, "already the active account") {
		t.Fatalf("message = %q", d.message)
	}
}

func TestDashboard_ReadOnlyWithoutASwitcher(t *testing.T) {
	d, _ := newTestDashboard([]*MonitorState{stateWith(codexProfile("a", false))}, nil)
	load(t, d)
	_, cmd := d.Update(key("enter"))
	if d.mode != modeBrowse || cmd != nil || !d.messageErr {
		t.Fatalf("read-only dashboard accepted a switch: mode=%v", d.mode)
	}
}

func TestDashboard_KeepsLastKnownUsageWhenAFetchFails(t *testing.T) {
	good := stateWith(claudeProfile("chris", true))
	failed := stateWith(&ProfileState{
		Provider: "claude", ProfileName: "chris", Active: true,
		Usage: &usage.UsageInfo{Provider: "claude", ProfileName: "chris", Error: "unauthorized: token expired", FetchedAt: dashNow().Add(12 * time.Minute)},
	})
	later := dashNow().Add(12 * time.Minute)
	ref := &fakeRefresher{states: []*MonitorState{good, failed}}
	d := NewDashboard(ref, DashboardOptions{Interval: time.Minute, Now: func() time.Time { return later }})
	d.width = 200

	load(t, d)
	if d.Rows()[0].Stale {
		t.Fatalf("first fetch marked stale")
	}
	load(t, d)
	row := d.Rows()[0]
	if !row.Stale || row.Usage == nil || row.Usage.PrimaryWindow == nil {
		t.Fatalf("last known usage not retained: %+v", row)
	}
	view := d.View()
	if !strings.Contains(view, "82% left") || !strings.Contains(view, "last known 12m ago: auth expired (re-login)") {
		t.Fatalf("stale row not rendered with its age:\n%s", view)
	}
}

func TestDashboard_ErrorRowWithNoHistoryShowsTheReason(t *testing.T) {
	failed := stateWith(&ProfileState{
		Provider: "zcode", ProfileName: "chris",
		Usage: &usage.UsageInfo{Provider: "zcode", ProfileName: "chris", Error: "no Z.ai coding plan on this account", FetchedAt: dashNow()},
	})
	d, _ := newTestDashboard([]*MonitorState{failed}, nil)
	load(t, d)
	view := d.View()
	// The reason is the same short phrase the tui's card shows (usage.ShortError).
	if !strings.Contains(view, "zcode/chris") || !strings.Contains(view, "no coding plan") {
		t.Fatalf("error row missing its reason:\n%s", view)
	}
	if len(d.Columns()) != 0 {
		t.Fatalf("an error-only table should have no window columns: %v", d.Columns())
	}
}

func TestDashboard_TickRefreshesUnlessBusy(t *testing.T) {
	d, ref := newTestDashboard([]*MonitorState{stateWith(codexProfile("a", true))}, nil)
	d.opts.Interval = 20 * time.Millisecond // so a lone next-tick timer resolves quickly
	load(t, d)

	_, cmd := d.Update(tickMsg(dashNow()))
	if cmd == nil {
		t.Fatalf("tick scheduled nothing")
	}
	// The batch holds a refresh and the next tick; run the refresh half by
	// checking the refresher saw another call once the batch runs.
	runBatch(t, cmd, d)
	if ref.refreshs != 2 {
		t.Fatalf("tick did not refresh: refreshes=%d", ref.refreshs)
	}

	// While a switch is in flight the tick must not refresh.
	d.mode = modeSwitching
	_, cmd = d.Update(tickMsg(dashNow()))
	runBatch(t, cmd, d)
	if ref.refreshs != 2 {
		t.Fatalf("tick refreshed during a switch: refreshes=%d", ref.refreshs)
	}
}

// runBatch executes the commands in a tea.Batch that complete immediately
// (a refresh), skipping timers (the next tick).
func runBatch(t *testing.T, cmd tea.Cmd, d *Dashboard) {
	t.Helper()
	if cmd == nil {
		return
	}
	msg := cmd()
	switch m := msg.(type) {
	case tea.BatchMsg:
		for _, c := range m {
			if c == nil {
				continue
			}
			done := make(chan tea.Msg, 1)
			go func(c tea.Cmd) { done <- c() }(c)
			select {
			case r := <-done:
				if _, isTick := r.(tickMsg); !isTick {
					d.Update(r)
				}
			case <-time.After(200 * time.Millisecond):
				// a timer; leave it
			}
		}
	case refreshDoneMsg:
		d.Update(m)
	}
}

func TestDashboard_CursorSurvivesRefreshAndStaysInRange(t *testing.T) {
	first := stateWith(codexProfile("a", true), codexProfile("b", false), codexProfile("c", false))
	second := stateWith(codexProfile("a", true), codexProfile("c", false))
	d, _ := newTestDashboard([]*MonitorState{first, second}, nil)
	load(t, d)

	d.Update(key("down"))
	d.Update(key("down"))
	if d.cursor != 2 || d.Rows()[d.cursor].Name != "c" {
		t.Fatalf("cursor = %d", d.cursor)
	}
	d.Update(key("down"))
	if d.cursor != 2 {
		t.Fatalf("cursor ran past the last row: %d", d.cursor)
	}

	load(t, d) // b disappears; the selection follows c
	if d.cursor != 1 || d.Rows()[d.cursor].Name != "c" {
		t.Fatalf("cursor after refresh = %d (%s)", d.cursor, d.Rows()[d.cursor].Name)
	}
}

// nilStateRefresher answers the first refresh with a good snapshot and
// every later one with no snapshot at all, as a monitor that has not yet
// (or no longer) built one does.
type nilStateRefresher struct {
	first   *MonitorState
	fetches int
}

func (f *nilStateRefresher) Refresh(ctx context.Context) error { f.fetches++; return nil }

func (f *nilStateRefresher) GetState() *MonitorState {
	if f.fetches <= 1 {
		return f.first
	}
	return nil
}

func TestDashboard_KeepsRowsWhenARefreshYieldsNoSnapshot(t *testing.T) {
	ref := &nilStateRefresher{first: stateWith(claudeProfile("chris", true))}
	d := NewDashboard(ref, DashboardOptions{Interval: time.Minute, Now: dashNow})
	d.width = 200
	load(t, d)
	if len(d.Rows()) != 1 {
		t.Fatalf("first fetch produced %d rows, want 1", len(d.Rows()))
	}
	load(t, d) // must not panic
	if len(d.Rows()) != 1 || d.Rows()[0].Usage == nil {
		t.Fatalf("rows lost when the refresh yielded no snapshot: %+v", d.Rows())
	}
}
