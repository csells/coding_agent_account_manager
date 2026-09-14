package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/sync"
)

// s in the sync panel copies vault credentials to and from another machine
// over SSH. That is asked in a dialog naming the machine, never done from
// a bare keypress; cancelling sends nothing and leaves the panel up.
func TestSyncSendAsksFirst(t *testing.T) {
	m := modelWithTwoClaudeProfiles(Hooks{})
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("S")})
	m = updated.(Model)
	if m.syncPanel == nil || !m.syncPanel.Visible() {
		t.Fatalf("S should open the sync panel")
	}
	state := &sync.SyncState{Pool: sync.NewSyncPool()}
	state.Pool.AddMachine(sync.NewMachine("laptop", "192.0.2.10"))
	m.syncPanel.SetState(state)

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	m = updated.(Model)
	if cmd != nil || m.state != stateConfirm || m.confirmDialog == nil || m.syncPanel.Syncing() {
		t.Fatalf("s should ask before syncing: cmd=%v state=%v syncing=%v", cmd, m.state, m.syncPanel.Syncing())
	}
	view := ansi.Strip(m.View())
	for _, want := range []string{"laptop", "192.0.2.10", "SSH", "credential"} {
		if !strings.Contains(view, want) {
			t.Errorf("the sync question lacks %q:\n%s", want, view)
		}
	}

	// Esc: nothing is sent, and the panel is still there.
	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEscape})
	m = updated.(Model)
	if cmd != nil || m.state != stateList || m.syncPanel.Syncing() || !m.syncPanel.Visible() {
		t.Fatalf("esc should send nothing: cmd=%v state=%v syncing=%v visible=%v", cmd, m.state, m.syncPanel.Syncing(), m.syncPanel.Visible())
	}

	// Yes: the sync starts. The command is not run — it would open SSH.
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	m = updated.(Model)
	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	m = updated.(Model)
	if cmd == nil || !m.syncPanel.Syncing() || m.state != stateList {
		t.Fatalf("yes should start the sync: cmd=%v syncing=%v state=%v", cmd, m.syncPanel.Syncing(), m.state)
	}
}

func TestSyncPanel_View_Empty(t *testing.T) {
	p := NewSyncPanel()
	p.SetSize(120, 40)

	out := p.View()
	if out == "" {
		t.Fatalf("View() returned empty")
	}
	if want := "No machines"; !strings.Contains(out, want) {
		t.Fatalf("View() output missing %q, got: %s", want, out)
	}
}

func TestSyncPanel_Toggle(t *testing.T) {
	p := NewSyncPanel()

	if p.Visible() {
		t.Fatalf("panel should not be visible initially")
	}

	p.Toggle()
	if !p.Visible() {
		t.Fatalf("panel should be visible after toggle")
	}

	p.Toggle()
	if p.Visible() {
		t.Fatalf("panel should not be visible after second toggle")
	}
}

func TestSyncPanel_Navigation(t *testing.T) {
	p := NewSyncPanel()

	// Can't move up/down with no machines
	p.MoveUp()
	p.MoveDown()
	// Should not panic
}

func TestModel_SyncPanel_ToggleWithKey(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("CAAM_HOME", tmpDir)

	m := New()
	m.width = 120
	m.height = 40

	// Toggle on with 'S'
	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("S")})
	m = model.(Model)
	if m.syncPanel == nil || !m.syncPanel.Visible() {
		t.Fatalf("sync panel not visible after toggle")
	}

	// Close with esc
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyEscape})
	m = model.(Model)
	if m.syncPanel.Visible() {
		t.Fatalf("sync panel still visible after esc")
	}
}

func TestSyncPanel_SetLoading(t *testing.T) {
	p := NewSyncPanel()
	p.SetSize(120, 40)
	p.SetLoading(true)

	out := p.View()
	if !strings.Contains(out, "Loading") {
		t.Fatalf("View() should show loading state, got: %s", out)
	}

	p.SetLoading(false)
	out = p.View()
	if strings.Contains(out, "Loading") {
		t.Fatalf("View() should not show loading after SetLoading(false)")
	}
}

func TestSyncPanel_LoadingSnapshot(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	theme := NewTheme(ThemeOptionsFromEnv())
	p := NewSyncPanelWithTheme(theme)
	p.SetLoading(true)

	got := normalizeSyncSnapshot(p.View())

	// Verify essential content is present (breadcrumb, title, status, loading indicator)
	if !strings.Contains(got, "Profiles > Sync") {
		t.Errorf("missing breadcrumb 'Profiles > Sync'")
	}
	if !strings.Contains(got, "[Esc] Back") {
		t.Errorf("missing back hint '[Esc] Back'")
	}
	if !strings.Contains(got, "Sync Pool") {
		t.Errorf("missing title 'Sync Pool'")
	}
	if !strings.Contains(got, "Status:") {
		t.Errorf("missing 'Status:' line")
	}
	if !strings.Contains(got, "Loading sync state") {
		t.Errorf("missing loading indicator")
	}
}

func TestSyncPanel_SetSyncing(t *testing.T) {
	p := NewSyncPanel()
	p.SetSyncing(true)
	// Should not panic

	// Test nil receiver
	var nilPanel *SyncPanel
	nilPanel.SetSyncing(true)
	// Should not panic
}

func TestSyncPanel_SetState(t *testing.T) {
	p := NewSyncPanel()
	p.SetSize(120, 40)

	// Set nil state
	p.SetState(nil)
	if p.State() != nil {
		t.Fatal("State should be nil after SetState(nil)")
	}

	// Test nil receiver
	var nilPanel *SyncPanel
	nilPanel.SetState(nil)
	// Should not panic
}

func TestSyncPanel_State(t *testing.T) {
	p := NewSyncPanel()
	if p.State() != nil {
		t.Fatal("State should be nil initially")
	}

	// Test nil receiver
	var nilPanel *SyncPanel
	if nilPanel.State() != nil {
		t.Fatal("State on nil receiver should return nil")
	}
}

func TestSyncPanel_SelectedMachine(t *testing.T) {
	p := NewSyncPanel()

	// No machines - should return nil
	if m := p.SelectedMachine(); m != nil {
		t.Fatal("SelectedMachine should return nil with no machines")
	}

	// Test nil receiver
	var nilPanel *SyncPanel
	if m := nilPanel.SelectedMachine(); m != nil {
		t.Fatal("SelectedMachine on nil receiver should return nil")
	}
}

func normalizeSyncSnapshot(s string) string {
	plain := ansi.Strip(s)
	lines := strings.Split(plain, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " ")
	}
	return strings.Join(lines, "\n")
}

// Note: TestGetStatusIcon, TestFormatTimeAgo, TestTruncateString, TestToMachineInfo
// are defined in sync_test.go
