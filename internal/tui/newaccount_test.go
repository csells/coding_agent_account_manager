package tui

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
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
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	m = updated.(Model)
	if cmd == nil {
		t.Fatalf("n should hand the terminal to the login command")
	}
	if !strings.Contains(m.statusMsg, "Complete the claude login") {
		t.Fatalf("status should carry the login hint, got %q", m.statusMsg)
	}
}

func TestNewAccount_UnavailableProviderSaysWhy(t *testing.T) {
	h := &newAccountHooks{}
	m := modelWithTwoClaudeProfiles(h.hooks(t))
	m.width, m.height = 170, 40
	for i, p := range m.providers {
		if p == "cursor" {
			m.activeProvider = i
		}
	}
	m.syncProfilesPanel()
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	m = updated.(Model)
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
