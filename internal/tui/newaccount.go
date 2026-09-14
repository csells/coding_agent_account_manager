package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	caamdb "github.com/Dicklesworthstone/coding_agent_account_manager/internal/db"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/switcher"
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

// providerChoice is one row of the provider picker.
type providerChoice struct {
	id    string
	label string
	note  string // "2 accounts", "not installed (…)"
	ok    bool   // the provider's CLI can be run
}

// ProviderPickerDialog asks which provider to log in to. It lists every
// provider caam manages, not only those on the strip, since the point is
// usually to add the first account of one.
type ProviderPickerDialog struct {
	choices  []providerChoice
	selected int
	result   DialogResult
	styles   Styles
	width    int
}

// NewProviderPickerDialog builds a picker over choices, preselecting id.
func NewProviderPickerDialog(choices []providerChoice, id string) *ProviderPickerDialog {
	d := &ProviderPickerDialog{choices: choices}
	for i, c := range choices {
		if c.id == id {
			d.selected = i
		}
	}
	return d
}

// SetStyles sets the dialog styles.
func (d *ProviderPickerDialog) SetStyles(styles Styles) { d.styles = styles }

// SetWidth sets the dialog width.
func (d *ProviderPickerDialog) SetWidth(width int) { d.width = width }

// Result reports whether the picker was submitted or cancelled.
func (d *ProviderPickerDialog) Result() DialogResult { return d.result }

// Chosen returns the selected provider id.
func (d *ProviderPickerDialog) Chosen() string {
	if d.selected < 0 || d.selected >= len(d.choices) {
		return ""
	}
	return d.choices[d.selected].id
}

// Update moves the selection with ↑/↓ (or k/j), submits on enter and
// cancels on esc.
func (d *ProviderPickerDialog) Update(msg tea.KeyMsg) {
	switch msg.Type {
	case tea.KeyEscape, tea.KeyCtrlC:
		d.result = DialogResultCancel
	case tea.KeyEnter:
		if len(d.choices) > 0 {
			d.result = DialogResultSubmit
		}
	case tea.KeyUp:
		d.move(-1)
	case tea.KeyDown:
		d.move(1)
	case tea.KeyRunes:
		switch string(msg.Runes) {
		case "k":
			d.move(-1)
		case "j":
			d.move(1)
		case "q":
			d.result = DialogResultCancel
		}
	}
}

func (d *ProviderPickerDialog) move(delta int) {
	if n := len(d.choices); n > 0 {
		d.selected = (d.selected + delta + n) % n
	}
}

// View renders the picker: one line per provider, the selected one marked.
func (d *ProviderPickerDialog) View() string {
	var b strings.Builder
	b.WriteString(d.styles.DialogTitle.Render("Log in to which provider?"))
	b.WriteString("\n\n")
	labelWidth := 0
	for _, c := range d.choices {
		if w := lipgloss.Width(c.label); w > labelWidth {
			labelWidth = w
		}
	}
	for i, c := range d.choices {
		name := d.styles.Item
		if i == d.selected {
			name = d.styles.SelectedItem
		}
		note := d.styles.StatusText
		if !c.ok {
			note = d.styles.StatusWarning
		}
		marker := "  "
		if i == d.selected {
			marker = "▸ "
		}
		b.WriteString(marker + name.Render(padRight(c.label, labelWidth)) + "  " + note.Render(c.note))
		if i < len(d.choices)-1 {
			b.WriteString("\n")
		}
	}
	b.WriteString("\n\n")
	b.WriteString(d.styles.StatusKey.Render(" ↑/↓ ") + d.styles.StatusText.Render(" choose  ") +
		d.styles.StatusKey.Render(" enter ") + d.styles.StatusText.Render(" log in  ") +
		d.styles.StatusKey.Render(" esc ") + d.styles.StatusText.Render(" cancel"))
	return d.styles.DialogFocused.Width(d.width).Render(b.String())
}

// handleNewAccount (n) asks which provider to log in to. Every provider
// is offered, with the selected one preselected; providers whose CLI is
// not installed say so but stay selectable, so the reason is one keypress
// away.
func (m Model) handleNewAccount() (tea.Model, tea.Cmd) {
	if m.hooks.Login == nil {
		m.statusMsg = "Logging in from the dashboard is not available in this build"
		return m, nil
	}
	all := m.allProviders
	if len(all) == 0 {
		all = m.providers
	}
	choices := make([]providerChoice, 0, len(all))
	for _, id := range all {
		c := providerChoice{id: id, label: providerLabel(id), ok: true}
		switch n := len(m.profiles[id]); n {
		case 0:
			c.note = "no accounts yet"
		case 1:
			c.note = "1 account"
		default:
			c.note = fmt.Sprintf("%d accounts", n)
		}
		if _, _, err := m.hooks.Login(id); err != nil {
			c.ok = false
			c.note = err.Error()
		}
		choices = append(choices, c)
	}
	m.providerPicker = NewProviderPickerDialog(choices, m.currentProvider())
	m.providerPicker.SetStyles(m.styles)
	m.providerPicker.SetWidth(m.dialogWidth(60))
	m.state = stateProviderPicker
	m.statusMsg = ""
	return m, nil
}

// handleProviderPickerKeys drives the picker; a submitted choice starts
// that provider's login.
func (m Model) handleProviderPickerKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.providerPicker == nil {
		m.state = stateList
		return m, nil
	}
	m.providerPicker.Update(msg)
	switch m.providerPicker.Result() {
	case DialogResultSubmit:
		provider := m.providerPicker.Chosen()
		m.providerPicker = nil
		m.state = stateList
		return m.startNewAccountLogin(provider)
	case DialogResultCancel:
		m.providerPicker = nil
		m.state = stateList
		m.statusMsg = "Login cancelled"
	}
	return m, nil
}

// startNewAccountLogin re-captures the provider's signed-in account, then
// hands the terminal to its native login.
func (m Model) startNewAccountLogin(provider string) (tea.Model, tea.Cmd) {
	if provider == "" {
		m.statusMsg = "No provider selected"
		return m, nil
	}
	cmd, hint, err := m.hooks.Login(provider)
	if err != nil {
		m.showMessage(StatusError, "Cannot log in", "%s: %v", providerLabel(provider), err)
		return m, nil
	}

	// A login is a logout first (docs/ACCOUNT_SWITCHER.md §2): the
	// shared PrepareLogin vaults the signed-in account with its newest
	// tokens and clears the live credential, so the agent's login has
	// nothing to revoke. A failure means the login is not run.
	if fileSet, ok := authFileSetForProvider(provider); ok {
		if _, err := switcher.PrepareLogin(authfile.NewVault(m.vaultPath), fileSet); err != nil {
			m.setNotice(provider, "", err.Error(), true)
			m.showMessage(StatusError, "Login not started", "%v", err)
			return m, nil
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
		m.showMessage(StatusError, "Login did not complete", "%s: %v. The previous account is in the vault; select it and press enter to restore it.", providerLabel(msg.provider), msg.err)
		return m, m.refreshProfiles(refreshContext{provider: msg.provider})
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
		return m.openNameDialog(msg.provider)
	}
	fileSet, _ := authFileSetForProvider(msg.provider)
	provider := msg.provider
	if err := m.finishLogin(fileSet, msg.name, func(name string) error { return m.captureLive(provider, name) }); err != nil {
		m.setNotice(msg.provider, msg.name, err.Error(), true)
		m.showMessage(StatusError, "Capture failed", "%v", err)
		return m, nil
	}
	m.selectedProfileName = msg.name
	delete(m.limits, limitsKey(msg.provider, msg.name))
	m.setNotice(msg.provider, msg.name, "Logged in and captured "+msg.name, false)
	m.showMessage(StatusSuccess, "Logged in", "%s now uses %s, and its credential is in the vault.", providerLabel(msg.provider), msg.name)
	return m, m.refreshProfiles(refreshContext{provider: msg.provider, selectedProfile: msg.name})
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

// finishLogin files the new session under name through the shared
// FinishLogin, which also logs the login (the LAST USED column's source).
// The activity log is optional: a missing database is not an error.
func (m Model) finishLogin(fileSet authfile.AuthFileSet, name string, capture func(string) error) error {
	var db *caamdb.DB
	if d, err := caamdb.Open(); err == nil {
		db = d
		defer db.Close()
	}
	_, err := switcher.FinishLogin(context.Background(), authfile.NewVault(m.vaultPath), fileSet, nil, switcher.LoginOptions{
		Name:    name,
		Capture: capture,
		DB:      db,
	})
	return err
}
