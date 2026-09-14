package tui

import (
	"context"
	"errors"
	caamdb "github.com/Dicklesworthstone/coding_agent_account_manager/internal/db"
	"os/exec"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// newAccountHooks fakes the command layer: a login that succeeds without
// running anything, an identity answer, and a capture recorder.
type newAccountHooks struct {
	identity string
	captured []string
	fail     map[string]error
}

func (h *newAccountHooks) hooks(t *testing.T) Hooks {
	t.Helper()
	return Hooks{
		Login: func(provider string) (*exec.Cmd, string, error) {
			if provider == "cursor" {
				return nil, "", errors.New("cursor is not installed (not on PATH)")
			}
			return exec.Command("true"), "Complete the " + provider + " login, then come back here.", nil
		},
		LiveIdentity: func(ctx context.Context, provider string) string { return h.identity },
		Capture: func(provider, name string) error {
			if err := h.fail[provider+"/"+name]; err != nil {
				return err
			}
			h.captured = append(h.captured, provider+"/"+name)
			return nil
		},
	}
}

func TestNewAccount_RecapturesTheActiveAccountThenRunsTheLogin(t *testing.T) {
	h := &newAccountHooks{identity: "new@example.com"}
	m := modelWithTwoClaudeProfiles(h.hooks(t))
	m.width, m.height = 170, 40
	// The vault has no live claude credential in the test HOME, so the
	// active-profile lookup finds nothing to re-capture; seed the vault's
	// notion of the active profile through the model instead.
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	m = updated.(Model)
	if m.state != stateProviderPicker || m.providerPicker == nil {
		t.Fatalf("n should ask which provider to log in to, state=%v", m.state)
	}
	// The picker offers every provider, preselecting the current one, and
	// says which are not installed.
	if got := m.providerPicker.Chosen(); got != "claude" {
		t.Fatalf("picker preselected %q, want the selected provider claude", got)
	}
	if len(m.providerPicker.choices) != len(m.allProviders) {
		t.Fatalf("picker lists %d providers, want all %d", len(m.providerPicker.choices), len(m.allProviders))
	}
	view := ansi.Strip(m.View())
	for _, want := range []string{"Log in to which provider?", "▸ Claude", "2 accounts", "Cursor", "not installed"} {
		if !strings.Contains(view, want) {
			t.Errorf("picker lacks %q:\n%s", want, view)
		}
	}
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	if cmd == nil || m.state != stateList {
		t.Fatalf("enter should hand the terminal to the login command (cmd=%v state=%v)", cmd, m.state)
	}
	if !strings.Contains(m.statusMsg, "Complete the claude login") {
		t.Fatalf("status should carry the login hint, got %q", m.statusMsg)
	}
}

// pickProvider opens the picker with n and submits the given provider.
func pickProvider(t *testing.T, m Model, id string) (Model, tea.Cmd) {
	t.Helper()
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	m = updated.(Model)
	if m.state != stateProviderPicker || m.providerPicker == nil {
		t.Fatalf("n should open the provider picker, state=%v", m.state)
	}
	for i := 0; i < len(m.providerPicker.choices) && m.providerPicker.Chosen() != id; i++ {
		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
		m = updated.(Model)
	}
	if m.providerPicker.Chosen() != id {
		t.Fatalf("could not select %s in the picker", id)
	}
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	return updated.(Model), cmd
}

func TestNewAccount_EscLeavesThePickerWithoutLoggingIn(t *testing.T) {
	h := &newAccountHooks{}
	m := modelWithTwoClaudeProfiles(h.hooks(t))
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	m = updated.(Model)
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEscape})
	m = updated.(Model)
	if cmd != nil || m.state != stateList || m.providerPicker != nil {
		t.Fatalf("esc should close the picker and start nothing: cmd=%v state=%v", cmd, m.state)
	}
}

func TestNewAccount_UnavailableProviderSaysWhy(t *testing.T) {
	h := &newAccountHooks{}
	m := modelWithTwoClaudeProfiles(h.hooks(t))
	m.width, m.height = 170, 40
	m, cmd := pickProvider(t, m, "cursor")
	if cmd != nil || !strings.Contains(m.statusMsg, "not installed") {
		t.Fatalf("an uninstallable provider should refuse with the reason: cmd=%v status=%q", cmd, m.statusMsg)
	}
}

func TestNewAccount_NoHooksSaysSo(t *testing.T) {
	m := modelWithTwoClaudeProfiles(Hooks{})
	m.width, m.height = 170, 40
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	m = updated.(Model)
	if cmd != nil || !strings.Contains(m.statusMsg, "not available") {
		t.Fatalf("without a login hook n must say so: cmd=%v status=%q", cmd, m.statusMsg)
	}
}

func TestNewAccount_IdentifiedAccountIsCapturedAndSelected(t *testing.T) {
	h := &newAccountHooks{identity: "new@example.com"}
	m := modelWithTwoClaudeProfiles(h.hooks(t))
	m.width, m.height = 170, 40

	// The login command returned cleanly: the identity is read next.
	updated, cmd := m.Update(newAccountLoginDoneMsg{provider: "claude"})
	m = updated.(Model)
	if cmd == nil {
		t.Fatalf("a finished login should start the identity lookup")
	}
	msg := cmd()
	ident, ok := msg.(newAccountIdentifiedMsg)
	if !ok || ident.name != "new@example.com" {
		t.Fatalf("identity lookup produced %#v", msg)
	}

	updated, cmd = m.Update(ident)
	m = updated.(Model)
	if len(h.captured) != 1 || h.captured[0] != "claude/new@example.com" {
		t.Fatalf("captured = %v, want the new account", h.captured)
	}
	if m.selectedProfileName != "new@example.com" {
		t.Fatalf("selection should move to the new account, got %q", m.selectedProfileName)
	}
	if !strings.Contains(m.notice, "Logged in and captured new@example.com") {
		t.Fatalf("notice = %q", m.notice)
	}
	if cmd == nil {
		t.Fatalf("the profile list should reload after the capture")
	}
	// The login is the account's first use on record.
	db, err := caamdb.Open()
	if err != nil {
		t.Fatalf("open activity log: %v", err)
	}
	defer db.Close()
	used, err := db.LastUsed()
	if err != nil || used["claude"]["new@example.com"].IsZero() {
		t.Fatalf("a captured login should be in the activity log: used=%v err=%v", used, err)
	}
}

func TestNewAccount_CaptureFailureIsSurfaced(t *testing.T) {
	h := &newAccountHooks{identity: "new@example.com", fail: map[string]error{"claude/new@example.com": errors.New("no Claude Code credential to back up")}}
	m := modelWithTwoClaudeProfiles(h.hooks(t))
	m.width, m.height = 170, 40
	updated, _ := m.Update(newAccountIdentifiedMsg{provider: "claude", name: "new@example.com"})
	m = updated.(Model)
	if !m.noticeErr || !strings.Contains(m.notice, "capturing it failed") {
		t.Fatalf("capture failure not surfaced: notice=%q err=%v", m.notice, m.noticeErr)
	}
	if m.selectedProfileName == "new@example.com" {
		t.Fatalf("selection must not move to an account that was not captured")
	}
}

func TestNewAccount_WithoutAnIdentityAsksForAName(t *testing.T) {
	h := &newAccountHooks{identity: ""}
	m := modelWithTwoClaudeProfiles(h.hooks(t))
	m.width, m.height = 170, 40
	// handleBackupProfile needs live auth files to exist for the provider;
	// the isolated test HOME has none, so it reports that instead of a
	// dialog — the message still tells the user what happened.
	updated, _ := m.Update(newAccountIdentifiedMsg{provider: "claude", name: ""})
	m = updated.(Model)
	if m.state != stateBackupDialog && !strings.Contains(m.statusMsg, "nothing to backup") {
		t.Fatalf("no identity should lead to naming the profile, got state=%v status=%q", m.state, m.statusMsg)
	}
}

func TestNewAccount_FailedLoginIsReported(t *testing.T) {
	h := &newAccountHooks{identity: "new@example.com"}
	m := modelWithTwoClaudeProfiles(h.hooks(t))
	updated, _ := m.Update(newAccountLoginDoneMsg{provider: "codex", err: errors.New("exit status 1")})
	m = updated.(Model)
	if !strings.Contains(m.statusMsg, "did not complete") || len(h.captured) != 0 {
		t.Fatalf("a failed login must not capture: status=%q captured=%v", m.statusMsg, h.captured)
	}
}

// A status line about one provider (here a login failure) must not follow
// the user to the next tab: it belongs to the provider it was written under.
func TestNewAccount_ErrorLeavesWithTheTab(t *testing.T) {
	h := &newAccountHooks{}
	m := modelWithTwoClaudeProfiles(h.hooks(t))
	m.profiles["cursor"] = []Profile{{Name: "c@example.com", Provider: "cursor"}}
	m.profiles["opencode"] = []Profile{{Name: "o@example.com", Provider: "opencode"}}
	m.syncProfilesPanel()
	for m.currentProvider() != "cursor" {
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRight})
		m = updated.(Model)
	}

	m, _ = pickProvider(t, m, "cursor")
	if !strings.Contains(m.statusMsg, "Cannot log in to Cursor") {
		t.Fatalf("status = %q, want the Cursor login failure", m.statusMsg)
	}

	// opencode sits just left of cursor on the strip.
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyLeft})
	m = updated.(Model)
	if m.currentProvider() != "opencode" {
		t.Fatalf("provider = %q, want opencode", m.currentProvider())
	}
	if m.statusMsg != "" {
		t.Fatalf("status = %q after moving to another tab, want it cleared", m.statusMsg)
	}
}

// b re-captures the selected account from the live credential — but only
// when it is the signed-in one, since the live credential is nobody else's.
func TestRecapture_TakesTheSignedInAccountOnly(t *testing.T) {
	h := &newAccountHooks{}
	m := modelWithTwoClaudeProfiles(h.hooks(t))
	m.width, m.height = 170, 40

	// a@example.com is active and selected.
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("b")})
	m = updated.(Model)
	if len(h.captured) != 1 || h.captured[0] != "claude/a@example.com" {
		t.Fatalf("b should re-capture the signed-in account, captured %v", h.captured)
	}
	if m.state != stateList || !strings.Contains(m.notice, "Re-captured a@example.com") {
		t.Fatalf("state=%v notice=%q, want a re-capture notice with no dialog", m.state, m.notice)
	}

	// b@example.com is not signed in: nothing live belongs to it.
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(Model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("b")})
	m = updated.(Model)
	if len(h.captured) != 1 {
		t.Fatalf("b on an inactive account must not capture, captured %v", h.captured)
	}
	if !m.noticeErr || !strings.Contains(m.notice, "a@example.com is signed in, not b@example.com") {
		t.Fatalf("notice = %q, want the reason it was refused", m.notice)
	}
	if m.state != stateList {
		t.Fatalf("no dialog should open, state=%v", m.state)
	}
}
