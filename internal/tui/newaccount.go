package tui

import (
	"context"
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
)

// The `n` key logs a NEW account into the selected provider without leaving
// the dashboard: the active account is re-captured first (its rotated
// tokens must be in the vault before the tool's login overwrites them),
// the terminal is handed to the provider's own login command, and when
// it returns the live credential is captured under the account's identity
// and selected. A provider whose credential carries no identity asks for
// a name instead.

// newAccountLoginDoneMsg: the native login command returned.
type newAccountLoginDoneMsg struct {
	provider string
	err      error
}

// newAccountIdentifiedMsg: the live credential's identity was read.
type newAccountIdentifiedMsg struct {
	provider string
	name     string
}

func (m Model) handleNewAccount() (tea.Model, tea.Cmd) {
	provider := m.currentProvider()
	if provider == "" {
		m.statusMsg = "No provider selected"
		return m, nil
	}
	if m.hooks.Login == nil {
		m.statusMsg = "Logging in from the dashboard is not available in this build"
		return m, nil
	}
	cmd, hint, err := m.hooks.Login(provider)
	if err != nil {
		m.statusMsg = fmt.Sprintf("Cannot log in to %s: %v", providerLabel(provider), err)
		return m, nil
	}

	// The login will replace the live credential. Whatever account holds
	// it now goes back into the vault first, newest tokens and all.
	if fileSet, ok := authFileSetForProvider(provider); ok {
		vault := authfile.NewVault(m.vaultPath)
		if active, _ := vault.ActiveProfile(fileSet); active != "" {
			if err := m.captureLive(provider, active); err != nil {
				m.setNotice(provider, active, fmt.Sprintf("Not starting a login: the active account %s could not be re-captured first (%v)", active, err), true)
				m.statusMsg = "Login not started: re-capture of the active account failed"
				return m, m.addToast(m.statusMsg, StatusError)
			}
		}
	}

	m.statusMsg = hint
	return m, tea.ExecProcess(cmd, func(err error) tea.Msg {
		return newAccountLoginDoneMsg{provider: provider, err: err}
	})
}

// newAccountLoggedIn follows the native login: read who is logged in now.
func (m Model) newAccountLoggedIn(msg newAccountLoginDoneMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.statusMsg = fmt.Sprintf("%s login did not complete: %v", providerLabel(msg.provider), msg.err)
		return m, m.addToast(m.statusMsg, StatusError)
	}
	identify := m.hooks.LiveIdentity
	provider := msg.provider
	m.statusMsg = "Login finished; reading which account is signed in…"
	return m, func() tea.Msg {
		name := ""
		if identify != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			name = identify(ctx, provider)
		}
		return newAccountIdentifiedMsg{provider: provider, name: name}
	}
}

// newAccountIdentified captures the live credential under its identity and
// selects the new profile; without an identity it asks for a name.
func (m Model) newAccountIdentified(msg newAccountIdentifiedMsg) (tea.Model, tea.Cmd) {
	if msg.name == "" {
		model, cmd := m.handleBackupProfile()
		if next, ok := model.(Model); ok && next.state == stateBackupDialog {
			next.statusMsg = "Logged in. Name the new profile:"
			return next, cmd
		}
		return model, cmd
	}
	if err := m.captureLive(msg.provider, msg.name); err != nil {
		m.setNotice(msg.provider, msg.name, "Logged in as "+msg.name+", but capturing it failed: "+err.Error(), true)
		m.statusMsg = "Capture failed: " + err.Error()
		return m, m.addToast(m.statusMsg, StatusError)
	}
	m.selectedProfileName = msg.name
	delete(m.limits, limitsKey(msg.provider, msg.name))
	m.setNotice(msg.provider, msg.name, "Logged in and captured "+msg.name, false)
	m.statusMsg = fmt.Sprintf("Logged in to %s as %s", providerLabel(msg.provider), msg.name)
	return m, tea.Batch(m.addToast(m.statusMsg, StatusSuccess), m.refreshProfiles(refreshContext{provider: msg.provider, selectedProfile: msg.name}))
}

// captureLive vaults the provider's live credential under name, through
// the command layer's hook when there is one.
func (m Model) captureLive(provider, name string) error {
	if m.hooks.Capture != nil {
		return m.hooks.Capture(provider, name)
	}
	fileSet, ok := authFileSetForProvider(provider)
	if !ok {
		return fmt.Errorf("unknown provider %s", provider)
	}
	return authfile.NewVault(m.vaultPath).Backup(fileSet, name)
}
