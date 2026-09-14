package tui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/health"
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
	if v := m.detailPanel.View(); !strings.Contains(v, "auth expired") {
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

func TestHealthHookDrivesTheListStatus(t *testing.T) {
	// The stored snapshot knows nothing about these profiles; the hook does.
	hooks := Hooks{Health: func(provider, profile string) *health.ProfileHealth {
		switch profile {
		case "fresh@example.com":
			return &health.ProfileHealth{TokenExpiresAt: time.Now().Add(3 * time.Hour), PlanType: "max"}
		case "renews@example.com":
			return &health.ProfileHealth{TokenExpiresAt: time.Now().Add(-time.Hour), SelfRefreshing: true}
		}
		return nil
	}}
	m := New()
	m.width, m.height = 160, 40
	m.hooks = hooks
	vault := authfile.NewVault(m.vaultPath)
	for _, name := range []string{"fresh@example.com", "renews@example.com"} {
		dir := vault.ProfilePath("agy", name)
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "antigravity-oauth-token"), []byte(`{"access_token":"x"}`), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for i, p := range m.providers {
		if p == "agy" {
			m.activeProvider = i
		}
	}

	msg := m.loadProfiles()
	loaded, ok := msg.(profilesLoadedMsg)
	if !ok {
		t.Fatalf("loadProfiles returned %T", msg)
	}
	updated, _ := m.Update(loaded)
	m = updated.(Model)

	view := m.profilesPanel.View()
	if !strings.Contains(view, "Antigravity Profiles") {
		t.Errorf("provider label not Antigravity:\n%s", view)
	}
	if strings.Contains(view, "Unknown") {
		t.Errorf("hook-computed health still reads Unknown:\n%s", view)
	}
	if !strings.Contains(view, "Auto-refresh") {
		t.Errorf("self-renewing lapsed token should read Auto-refresh, as caam ls does:\n%s", view)
	}
	if strings.Contains(view, "Expired") {
		t.Errorf("self-renewing token flagged Expired:\n%s", view)
	}
}

func TestProviderLabels(t *testing.T) {
	cases := map[string]string{
		"agy": "Antigravity", "kimi": "Kimi Code", "zcode": "zcode", "opencode": "OpenCode",
		"claude": "Claude", "codex": "Codex", "gemini": "Gemini", "grok": "Grok", "cursor": "Cursor",
	}
	for id, want := range cases {
		if got := providerLabel(id); got != want {
			t.Errorf("providerLabel(%q) = %q, want %q", id, got, want)
		}
	}
}

// TestSelectedRowBackgroundCoversEveryCell: the selection highlight used to
// stop at the first cell with its own style, which on the active row is the
// green dot — so selecting the active account highlighted two characters.
// Every cell of the selected row must carry the row background, active or
// not, and unselected rows must carry none of it.
func TestSelectedRowBackgroundCoversEveryCell(t *testing.T) {
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(prev) })

	panel := NewProfilesPanel()
	panel.SetSize(140, 20)
	panel.SetProvider("claude")
	panel.SetProfiles([]ProfileInfo{
		{Name: "idle@example.com", AuthMode: "oauth", HealthStatus: health.StatusHealthy, Account: "idle@example.com"},
		{Name: "live@example.com", AuthMode: "oauth", HealthStatus: health.StatusHealthy, Account: "live@example.com", IsActive: true},
	})

	bg := sgrBackground(t, panel.styles.SelectedRow)

	for _, sel := range []int{0, 1} {
		panel.SetSelected(sel)
		lines := strings.Split(panel.View(), "\n")
		var selectedLine, otherLine string
		for _, l := range lines {
			switch {
			case strings.Contains(l, panel.profiles[sel].Name):
				selectedLine = l
			case strings.Contains(l, panel.profiles[1-sel].Name):
				otherLine = l
			}
		}
		if selectedLine == "" || otherLine == "" {
			t.Fatalf("rows not found in view:\n%s", panel.View())
		}
		// Every visible cell of the selected row sits inside a span that set
		// the row background: name, auth, status, last used, account.
		for _, cell := range []string{panel.profiles[sel].Name, "oauth", "Healthy", "never", "@example.com"} {
			if !spanHasBackground(selectedLine, cell, bg) {
				t.Errorf("selected=%d: cell %q lacks the row background:\n%q", sel, cell, selectedLine)
			}
		}
		if strings.Contains(otherLine, bg) {
			t.Errorf("selected=%d: unselected row carries the selection background:\n%q", sel, otherLine)
		}
	}
}

// sgrBackground extracts the background SGR parameters a style emits.
func sgrBackground(t *testing.T, s lipgloss.Style) string {
	t.Helper()
	rendered := lipgloss.NewStyle().Background(s.GetBackground()).Render("x")
	i := strings.Index(rendered, "48;")
	if i < 0 {
		t.Fatalf("style emits no background: %q", rendered)
	}
	j := strings.Index(rendered[i:], "m")
	return rendered[i : i+j]
}

// spanHasBackground reports whether the styled span that contains text was
// opened with the given background parameters (no reset in between).
func spanHasBackground(line, text, bg string) bool {
	idx := strings.Index(line, text)
	for idx >= 0 {
		before := line[:idx]
		if reset := strings.LastIndex(before, "\x1b[0m"); reset >= 0 {
			before = before[reset:]
		}
		if strings.Contains(before, bg) {
			return true
		}
		next := strings.Index(line[idx+1:], text)
		if next < 0 {
			break
		}
		idx += 1 + next
	}
	return false
}

// TestSelectionColourDiffersFromZebraStripe: the profile list stripes odd
// rows with SurfaceMuted, so a Selection of the same colour cannot be told
// from a stripe. Every palette, both modes.
func TestSelectionColourDiffersFromZebraStripe(t *testing.T) {
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(prev) })

	for _, contrast := range []ThemeContrast{ContrastNormal, ContrastHigh} {
		for _, mode := range []ThemeMode{ThemeLight, ThemeDark} {
			pal := paletteFor(ThemeOptions{Mode: mode, Contrast: contrast})
			sel := lipgloss.NewStyle().Background(pal.Selection).Render("x")
			alt := lipgloss.NewStyle().Background(pal.SurfaceMuted).Render("x")
			if sel == alt {
				t.Errorf("%v/%v: Selection renders the same as SurfaceMuted (%q)", contrast, mode, sel)
			}
		}
	}
}

// modelWithLimits is a two-account Claude model whose limits are already
// loaded, at the given terminal size.
func modelWithLimits(t *testing.T, w, h int) Model {
	t.Helper()
	rec := &limitsRecorder{info: sampleLimits()}
	m := modelWithTwoClaudeProfiles(Hooks{Limits: rec.fetch})
	m.width, m.height = w, h
	for _, name := range []string{"a@example.com", "b@example.com"} {
		m.applyLimitsLoaded(limitsLoadedMsg{provider: "claude", profile: name, info: sampleLimits()})
	}
	return m
}

func TestVerticalLayout_ProvidersAboveAccountsWithWindowColumns(t *testing.T) {
	m := modelWithLimits(t, 170, 40)
	view := stripANSI(m.View())

	iProviders := strings.Index(view, "Providers")
	iAccounts := strings.Index(view, "Claude accounts")
	if iProviders < 0 || iAccounts < 0 || iProviders > iAccounts {
		t.Fatalf("providers strip must sit above the accounts pane:\n%s", view)
	}
	for _, want := range []string{"▸ Claude (2)", "● a@example.com", "5h 82%", "wk 50%", "Fable 10%",
		"NAME", "STATUS", "5-HOUR", "WEEKLY", "WEEKLY FABLE", "LAST USED",
		"82% left · ", "50% left · ", "10% left", "enter", "switch to this account"} {
		if !strings.Contains(view, want) {
			t.Errorf("wide view lacks %q:\n%s", want, view)
		}
	}
	if lipgloss.Height(m.View()) > 40 {
		t.Errorf("view taller than the terminal: %d", lipgloss.Height(m.View()))
	}
}

func TestVerticalLayout_SelectedAccountExpandsInPlace(t *testing.T) {
	m := modelWithLimits(t, 170, 40)
	m.profiles["claude"] = append(m.profiles["claude"], Profile{Name: "c@example.com", Provider: "claude"})
	m.syncProfilesPanel()
	m.applyLimitsLoaded(limitsLoadedMsg{provider: "claude", profile: "c@example.com", info: sampleLimits()})

	// The first account is selected: its tree hangs between it and the
	// second row; the others are single rows.
	view := stripANSI(m.View())
	iA, iTree, iB, iC := strings.Index(view, "a@example.com"), strings.Index(view, "├─"), strings.Index(view, "  b@example.com"), strings.Index(view, "  c@example.com")
	if !(iA < iTree && iTree < iB && iB < iC) {
		t.Fatalf("expansion must sit under the selected row:\n%s", view)
	}
	if strings.Count(view, "└─") != 1 {
		t.Fatalf("exactly one expansion should be open:\n%s", view)
	}
	for _, want := range []string{"├─ oauth", "└─", "switch to this account", "~/vault/claude/a@example.com"} {
		if !strings.Contains(view, want) {
			t.Errorf("expansion lacks %q:\n%s", want, view)
		}
	}

	// ↓ moves the expansion to the next account.
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(Model)
	view = stripANSI(m.View())
	iA, iB, iTree = strings.Index(view, "  a@example.com"), strings.Index(view, "b@example.com"), strings.Index(view, "├─")
	if !(iA < iB && iB < iTree) || !strings.Contains(view, "~/vault/claude/b@example.com") {
		t.Fatalf("expansion did not follow the selection:\n%s", view)
	}
}

func TestVerticalLayout_ScrollsByAccountKeepingTheExpansionVisible(t *testing.T) {
	m := modelWithLimits(t, 170, 22) // few rows: header (2) + strip (7) + pane
	for _, n := range []string{"c", "d", "e", "f", "g", "h"} {
		m.profiles["claude"] = append(m.profiles["claude"], Profile{Name: n + "@example.com", Provider: "claude"})
	}
	m.syncProfilesPanel()
	for i := 0; i < 7; i++ {
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
		m = updated.(Model)
	}
	view := stripANSI(m.View())
	if !strings.Contains(view, "h@example.com") || !strings.Contains(view, "└─") {
		t.Fatalf("last account and its expansion must be in view:\n%s", view)
	}
	if lipgloss.Height(m.View()) > 22 {
		t.Fatalf("view taller than the terminal: %d", lipgloss.Height(m.View()))
	}
}

func TestVerticalLayout_MediumDropsLastUsedAndShortensCells(t *testing.T) {
	m := modelWithLimits(t, 120, 30)
	view := stripANSI(m.View())
	if strings.Contains(view, "LAST USED") {
		t.Errorf("medium tier should drop LAST USED:\n%s", view)
	}
	if !strings.Contains(view, "82% · ") || strings.Contains(view, "82% left") {
		t.Errorf("medium tier should show short window cells:\n%s", view)
	}
	if !strings.Contains(view, "▸ Claude 2") {
		t.Errorf("medium tier should show one-line provider chips:\n%s", view)
	}
	for _, line := range strings.Split(m.View(), "\n") {
		if lipgloss.Width(line) > 120 {
			t.Fatalf("line wider than the terminal (%d): %q", lipgloss.Width(line), stripANSI(line))
		}
	}
}

func TestVerticalLayout_NarrowShowsTightestWindowAndTabRow(t *testing.T) {
	m := modelWithLimits(t, 80, 24)
	view := stripANSI(m.View())
	for _, want := range []string{"TIGHTEST", "Fable 10%", "▸ Claude 2", "├─ 5-hour", "82% left", "├─ Weekly"} {
		if !strings.Contains(view, want) {
			t.Errorf("narrow view lacks %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "5-HOUR") {
		t.Errorf("narrow tier should not show every window column:\n%s", view)
	}
	for _, line := range strings.Split(m.View(), "\n") {
		if lipgloss.Width(line) > 80 {
			t.Fatalf("line wider than the terminal (%d): %q", lipgloss.Width(line), stripANSI(line))
		}
	}
	if lipgloss.Height(m.View()) > 24 {
		t.Errorf("view taller than the terminal: %d", lipgloss.Height(m.View()))
	}
}

func TestVerticalLayout_ArrowsSteerProvidersThenAccounts(t *testing.T) {
	m := modelWithLimits(t, 170, 40)
	m.profiles["codex"] = []Profile{{Name: "c@example.com", Provider: "codex", IsActive: true}}
	m.syncProfilesPanel()

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRight})
	m = updated.(Model)
	if m.currentProvider() != "codex" || !strings.Contains(stripANSI(m.View()), "Codex accounts") {
		t.Fatalf("→ should select the next provider's accounts, got %q", m.currentProvider())
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyLeft})
	m = updated.(Model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(Model)
	if m.currentProvider() != "claude" || m.selectedProfileInfo().Name != "b@example.com" {
		t.Fatalf("← then ↓ should select claude's second account, got %s/%v", m.currentProvider(), m.selectedProfileInfo())
	}
}

func TestVerticalLayout_DetailCardOverlayToggles(t *testing.T) {
	m := modelWithLimits(t, 170, 40)
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("i")})
	m = updated.(Model)
	if !m.showDetailCard || !strings.Contains(stripANSI(m.View()), "Profile: a@example.com") {
		t.Fatalf("i should open the full card for the selected account")
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(Model)
	if !m.showDetailCard || !strings.Contains(stripANSI(m.View()), "Profile: b@example.com") {
		t.Fatalf("↓ under the card should move it to the next account")
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(Model)
	if m.showDetailCard {
		t.Fatalf("esc should close the card")
	}
}

func TestVerticalLayout_PrefetchCoversTableAndStrip(t *testing.T) {
	rec := &limitsRecorder{info: sampleLimits()}
	m := modelWithTwoClaudeProfiles(Hooks{Limits: rec.fetch})
	m.profiles["codex"] = []Profile{{Name: "c@example.com", Provider: "codex", IsActive: true}, {Name: "d@example.com", Provider: "codex"}}
	m.syncProfilesPanel()

	cmd := m.limitsPrefetchCmd()
	if cmd == nil {
		t.Fatalf("prefetch returned nothing")
	}
	runCmd(t, m, cmd)
	// Both Claude accounts (the table) and Codex's active account (the
	// strip), but not Codex's idle one.
	for _, want := range []string{"claude/a@example.com", "claude/b@example.com", "codex/c@example.com"} {
		if rec.calls[want] != 1 {
			t.Errorf("%s fetched %d times, want 1 (calls=%v)", want, rec.calls[want], rec.calls)
		}
	}
	if rec.calls["codex/d@example.com"] != 0 {
		t.Errorf("idle account of another provider was fetched: %v", rec.calls)
	}
}

// TestVerticalLayout_NeverFillsTheLastColumn: a line that reaches the
// terminal's last column puts Terminal.app into its pending-wrap state and
// desynchronises Bubble Tea's row accounting — the provider strip's rows
// then render interleaved with the row beneath. Every line stays at least
// one column short, at every tier, with the selection anywhere.
func TestVerticalLayout_NeverFillsTheLastColumn(t *testing.T) {
	for _, size := range [][2]int{{170, 40}, {150, 40}, {120, 30}, {100, 24}, {80, 24}} {
		m := modelWithLimits(t, size[0], size[1])
		m.profiles["kimi"] = nil
		m.syncProfilesPanel()
		for step := 0; step < len(m.providers); step++ {
			for i, line := range strings.Split(m.View(), "\n") {
				// Trailing padding is tolerated (the status bar has always
				// spanned the width); visible content must stop short.
				content := strings.TrimRight(stripANSI(line), " ")
				if w := lipgloss.Width(content); w >= size[0] {
					t.Fatalf("%dx%d, provider %d, line %d fills the terminal width (%d): %q", size[0], size[1], step, i, w, content)
				}
			}
			updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
			m = updated.(Model)
		}
	}
}
