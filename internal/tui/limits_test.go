package tui

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/usage"
)

// limitsRecorder is a Hooks.Limits double that counts calls per profile.
type limitsRecorder struct {
	mu    sync.Mutex
	calls map[string]int
	info  *usage.UsageInfo
	err   error
}

func (r *limitsRecorder) fetch(ctx context.Context, provider, profile string) (*usage.UsageInfo, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.calls == nil {
		r.calls = make(map[string]int)
	}
	r.calls[provider+"/"+profile]++
	return r.info, r.err
}

func sampleLimits() *usage.UsageInfo {
	loc := time.FixedZone("PDT", -7*3600)
	return &usage.UsageInfo{
		Provider:        "claude",
		FetchedAt:       time.Now(),
		PrimaryWindow:   &usage.UsageWindow{UsedPercent: 18, ResetsAt: time.Now().Add(4 * time.Hour), WindowDuration: 5 * time.Hour},
		SecondaryWindow: &usage.UsageWindow{UsedPercent: 50, ResetsAt: time.Date(2030, 1, 1, 17, 0, 0, 0, loc), WindowDuration: 7 * 24 * time.Hour},
		ModelWindows: map[string]*usage.UsageWindow{
			"Fable": {UsedPercent: 90, Label: "Fable", WindowDuration: 7 * 24 * time.Hour, Severity: "critical"},
		},
	}
}

func modelWithTwoClaudeProfiles(hooks Hooks) Model {
	m := New()
	m.width, m.height = 160, 40
	m.hooks = hooks
	m.profiles = map[string][]Profile{
		"claude": {
			{Name: "a@example.com", Provider: "claude", IsActive: true},
			{Name: "b@example.com", Provider: "claude"},
		},
	}
	m.syncProfilesPanel()
	return m
}

// runCmd executes a command and feeds its message back, returning the model.
func runCmd(t *testing.T, m Model, cmd tea.Cmd) Model {
	t.Helper()
	if cmd == nil {
		return m
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			if c == nil {
				continue
			}
			updated, _ := m.Update(c())
			m = updated.(Model)
		}
		return m
	}
	updated, _ := m.Update(msg)
	return updated.(Model)
}

func TestDefaultProviders_AllHaveAnAuthFileSet(t *testing.T) {
	seen := map[string]bool{}
	for _, p := range DefaultProviders() {
		if _, ok := authfile.GetAuthFileSet(p); !ok {
			t.Errorf("provider %q has no auth file set", p)
		}
		seen[p] = true
	}
	for _, want := range []string{"agy", "kimi", "zcode"} {
		if !seen[want] {
			t.Errorf("provider %q missing from the TUI list", want)
		}
	}
}

func TestSelectionChangeFetchesLimitsOnceWithinTTL(t *testing.T) {
	rec := &limitsRecorder{info: sampleLimits()}
	m := modelWithTwoClaudeProfiles(Hooks{Limits: rec.fetch})

	// Moving to b fetches b's limits.
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(Model)
	if cmd == nil {
		t.Fatalf("moving the selection should start a limits fetch")
	}
	m = runCmd(t, m, cmd)
	if rec.calls["claude/b@example.com"] != 1 {
		t.Fatalf("fetch calls = %v", rec.calls)
	}

	// The detail card now carries the windows, as "left" with the reset.
	m.syncDetailPanel()
	view := m.detailPanel.View()
	for _, want := range []string{"Limits", "5-hour:", "82% left, resets", "Weekly:", "50% left", "Weekly Fable:", "10% left", "As of"} {
		if !strings.Contains(view, want) {
			t.Errorf("detail card lacks %q:\n%s", want, view)
		}
	}

	// Back to a and down to b again inside the TTL: no second fetch for b.
	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = runCmd(t, updated.(Model), cmd)
	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = runCmd(t, updated.(Model), cmd)
	if rec.calls["claude/b@example.com"] != 1 || rec.calls["claude/a@example.com"] != 1 {
		t.Fatalf("fetch calls after revisiting = %v, want one per profile", rec.calls)
	}
}

func TestLimitsSectionHiddenWithoutAFetcher(t *testing.T) {
	m := modelWithTwoClaudeProfiles(Hooks{})
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	if cmd != nil {
		t.Fatalf("no fetcher wired in, yet a command was returned")
	}
	m.syncDetailPanel()
	if strings.Contains(m.detailPanel.View(), "Limits") {
		t.Fatalf("Limits section rendered without a fetcher")
	}
}

func TestFailedLimitsFetchShowsReasonThenKeepsLastKnown(t *testing.T) {
	rec := &limitsRecorder{err: errors.New("unauthorized: token expired")}
	m := modelWithTwoClaudeProfiles(Hooks{Limits: rec.fetch})
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = runCmd(t, updated.(Model), cmd)
	m.syncDetailPanel()
	if v := m.detailPanel.View(); !strings.Contains(v, "unauthorized: token expired") {
		t.Fatalf("error not shown:\n%s", v)
	}

	// A later good fetch, then a failure: the good windows stay, marked.
	m.applyLimitsLoaded(limitsLoadedMsg{provider: "claude", profile: "b@example.com", info: sampleLimits()})
	m.applyLimitsLoaded(limitsLoadedMsg{provider: "claude", profile: "b@example.com", err: errors.New("unauthorized: token expired")})
	m.syncDetailPanel()
	v := m.detailPanel.View()
	if !strings.Contains(v, "82% left") || !strings.Contains(v, "last known") {
		t.Fatalf("last known windows not retained:\n%s", v)
	}
}

func TestEnterSwitchesThroughTheHook(t *testing.T) {
	var got []string
	hooks := Hooks{Switch: func(ctx context.Context, provider, profile string) error {
		got = append(got, provider+"/"+profile)
		return nil
	}}
	m := modelWithTwoClaudeProfiles(hooks)
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown}) // b, not active
	m = updated.(Model)

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	if m.state != stateConfirm || len(got) != 0 {
		t.Fatalf("enter should ask first: state=%v calls=%v", m.state, got)
	}
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	m = updated.(Model)
	if cmd == nil {
		t.Fatalf("confirming should start the switch")
	}
	msg := cmd()
	res, ok := msg.(activateResultMsg)
	if !ok || res.err != nil || res.provider != "claude" || res.profile != "b@example.com" {
		t.Fatalf("switch result = %#v", msg)
	}
	if len(got) != 1 || got[0] != "claude/b@example.com" {
		t.Fatalf("hook calls = %v", got)
	}
}

func TestEnterSurfacesASwitchRefusal(t *testing.T) {
	hooks := Hooks{Switch: func(ctx context.Context, provider, profile string) error {
		return errors.New("could not re-capture the outgoing profile a@example.com before switching")
	}}
	m := modelWithTwoClaudeProfiles(hooks)
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(Model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	m = runCmd(t, updated.(Model), cmd)
	if !strings.Contains(m.statusMsg, "re-capture") {
		t.Fatalf("refusal not surfaced: %q", m.statusMsg)
	}
}

func TestMainViewNeverExceedsTheTerminalHeight(t *testing.T) {
	rec := &limitsRecorder{info: sampleLimits()}
	m := modelWithTwoClaudeProfiles(Hooks{Limits: rec.fetch})
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = runCmd(t, updated.(Model), cmd)

	for _, h := range []int{20, 28, 40} {
		m.width, m.height = 200, h
		if got := lipgloss.Height(m.View()); got > h {
			t.Errorf("height %d: view is %d lines", h, got)
		}
		// The status bar is the last line, not something scrolled away.
		lines := strings.Split(m.View(), "\n")
		if last := lines[len(lines)-1]; !strings.Contains(last, "CLAUDE") {
			t.Errorf("height %d: status bar missing from the last line: %q", h, last)
		}
	}
}

func TestEnterRefusesACredentialLessProfileUpFront(t *testing.T) {
	called := false
	hooks := Hooks{Switch: func(ctx context.Context, provider, profile string) error {
		called = true
		return nil
	}}
	m := modelWithTwoClaudeProfiles(hooks)
	m.vaultMeta = map[string]map[string]vaultProfileMeta{
		"claude": {"b@example.com": {NoCredential: true}},
	}
	m.syncProfilesPanel()

	// The list says so before anything is pressed.
	if v := m.profilesPanel.View(); !strings.Contains(v, "No credential") {
		t.Fatalf("list does not flag the credential-less profile:\n%s", v)
	}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(Model)
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	if m.state == stateConfirm || called {
		t.Fatalf("enter should refuse, not confirm: state=%v called=%v", m.state, called)
	}
	if cmd == nil {
		t.Fatalf("refusal should raise a toast")
	}
	m.syncDetailPanel()
	v := flatCard(m.detailPanel.View())
	for _, want := range []string{"none captured", "caam backup claude b@example.com", "no captured credential"} {
		if !strings.Contains(v, want) {
			t.Errorf("detail card lacks %q:\n%s", want, v)
		}
	}

	// Moving to another profile clears the notice; coming back does not
	// resurrect it.
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = updated.(Model)
	m.syncDetailPanel()
	if strings.Contains(flatCard(m.detailPanel.View()), "no captured credential") {
		t.Fatalf("notice followed the selection to another profile")
	}
}

// flatCard collapses a rendered card to one line of words so a phrase that
// wrapped inside the box can still be matched.
func flatCard(v string) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(v, "│", " ")), " ")
}

func TestSwitchOutcomeShowsOnTheCard(t *testing.T) {
	m := modelWithTwoClaudeProfiles(Hooks{})
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(Model)

	updated, cmd := m.Update(activateResultMsg{provider: "claude", profile: "b@example.com",
		err: errors.New("profile claude/b@example.com holds no credential to install (settings only)")})
	m = updated.(Model)
	if cmd == nil {
		t.Fatalf("a failed switch should raise a toast")
	}
	m.syncDetailPanel()
	if v := flatCard(m.detailPanel.View()); !strings.Contains(v, "Activate failed") || !strings.Contains(v, "holds no credential") {
		t.Fatalf("failure not on the card:\n%s", v)
	}
}
