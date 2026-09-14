// Package tui provides the terminal user interface for caam.
package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/browser"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/config"
	caamdb "github.com/Dicklesworthstone/coding_agent_account_manager/internal/db"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/health"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/identity"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/profile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/project"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/refresh"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/signals"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/sync"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/usage"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/watcher"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// providerAccountURLs maps provider names to their account management URLs.
var providerAccountURLs = map[string]string{
	"claude": "https://console.anthropic.com/",
	"codex":  "https://platform.openai.com/",
	"gemini": "https://aistudio.google.com/",
}

// viewState represents the current view/mode of the TUI.
type viewState int

const (
	stateList viewState = iota
	stateDetail
	stateConfirm
	stateSearch
	stateHelp
	stateBackupDialog
	stateConfirmOverwrite
	stateExportConfirm
	stateImportPath
	stateImportConfirm
	stateEditProfile
	stateSyncAdd
	stateSyncEdit
	stateCommandPalette
	stateProviderPicker
)

const (
	dialogMinWidth = 24
	dialogMargin   = 4
)

// confirmAction represents the action being confirmed.
type confirmAction int

const (
	confirmNone confirmAction = iota
	confirmDelete
	confirmActivate
)

// Profile represents a saved auth profile for display.
type Profile struct {
	Name     string
	Provider string
	IsActive bool
}

type vaultProfileMeta struct {
	Description string
	Account     string
	// LastUsed is the last time the activity log saw this account in use:
	// switched to, switched away from, or logged in. Zero when never.
	LastUsed time.Time
	// NoCredential: the profile directory holds no file a credential can be
	// read from, so activating it would install settings and no login.
	NoCredential bool
}

// Model is the main Bubble Tea model for the caam TUI.
type Model struct {
	// Provider state
	// allProviders is every provider caam manages; providers is the subset
	// with at least one captured account, in the same order — the strip,
	// ←/→ and the accounts pane see only those.
	allProviders   []string
	providers      []string // codex, claude, gemini
	activeProvider int      // Currently selected provider index

	// Profile state
	profiles            map[string][]Profile // Profiles by provider
	selected            int                  // Currently selected profile index
	selectedProfileName string               // Selected profile name (source of truth for actions)
	profileStore        *profile.Store
	profileMeta         map[string]map[string]*profile.Profile
	vaultMeta           map[string]map[string]vaultProfileMeta

	// View state
	width  int
	height int
	state  viewState
	err    error

	// UI components
	keys          keyMap
	styles        Styles
	stripStyles   ProviderPanelStyles
	profilesPanel *ProfilesPanel
	detailPanel   *DetailPanel
	usagePanel    *UsagePanel
	syncPanel     *SyncPanel

	// Status message
	statusMsg string

	// Hot reload watcher
	vaultPath string
	watcher   *watcher.Watcher
	badges    map[string]profileBadge

	// Signal handling
	signals *signals.Handler

	// Runtime configuration
	runtime config.RuntimeConfig

	// Project context
	cwd            string
	projectStore   *project.Store
	projectContext *project.Resolved

	// Health storage for profile health data
	healthStorage *health.Storage

	// Confirmation state
	pendingAction confirmAction
	searchQuery   string

	// Dialog state for backup flow
	backupDialog *TextInputDialog
	// backupProvider is the provider the name dialog captures for; it can
	// differ from the selected one when the login came from the picker.
	backupProvider string
	providerPicker *ProviderPickerDialog
	confirmDialog  *ConfirmDialog
	pendingProfile string // Profile name pending overwrite confirmation
	editDialog     *MultiFieldDialog

	// Sync panel dialogs
	syncAddDialog       *MultiFieldDialog
	syncEditDialog      *MultiFieldDialog
	pendingSyncMachine  string
	pendingEditProvider string
	pendingEditProfile  string

	// Command palette dialog
	commandPalette *CommandPaletteDialog

	// Help renderer with Glamour markdown support and caching
	helpRenderer *HelpRenderer
	theme        Theme

	// Toast notifications
	toasts []Toast

	// Activity spinner for background operations (export/import)
	activitySpinner *Spinner
	activityMessage string // message to show with spinner

	// Operations delegated to the command layer (see Hooks), and the
	// per-profile rate-limit windows fetched through them.
	hooks  Hooks
	limits map[string]limitsEntry

	// notice is the outcome of the last action on noticeKey's profile,
	// shown on the detail card while that profile is selected.
	notice    string
	noticeErr bool
	noticeKey string

	// profileHealth is each profile's health verdict as computed when the
	// profiles were loaded (see computeHealthMap), keyed provider/name.
	profileHealth map[string]*health.ProfileHealth

	// showDetailCard overlays the full detail card for the selected
	// account (the `i` key); ↑/↓ move the selection underneath it.
	showDetailCard bool

	// stripOffset is the first provider slot the strip shows. It moves
	// only when the selection leaves the visible window (settleStrip).
	stripOffset int
}

// computeHealthMap builds the health verdict for every listed profile. With
// Hooks.Health it is the same verdict `caam ls` and `caam status` print —
// parsed from the profile's credential, live or vaulted — rather than the
// stored snapshot, which records an expiry only when something wrote one
// and so read "Unknown" for accounts the CLI called healthy. Without the
// hook the stored snapshot is what there is. It runs inside the load
// command, off the UI goroutine.
func (m Model) computeHealthMap(profiles map[string][]Profile) map[string]*health.ProfileHealth {
	out := make(map[string]*health.ProfileHealth)
	for provider, ps := range profiles {
		for _, p := range ps {
			var h *health.ProfileHealth
			if m.hooks.Health != nil {
				h = m.hooks.Health(provider, p.Name)
			} else if m.healthStorage != nil {
				if stored, err := m.healthStorage.GetProfile(provider, p.Name); err == nil {
					h = stored
				}
			}
			if h != nil {
				out[limitsKey(provider, p.Name)] = h
			}
		}
	}
	return out
}

// healthFor returns a profile's health verdict: the one computed at load
// time, else the stored snapshot.
func (m Model) healthFor(provider, name string) *health.ProfileHealth {
	if h, ok := m.profileHealth[limitsKey(provider, name)]; ok && h != nil {
		return h
	}
	if m.healthStorage != nil {
		if h, err := m.healthStorage.GetProfile(provider, name); err == nil && h != nil {
			return h
		}
	}
	return nil
}

// selectionKey identifies the selected provider/profile, or "" when none.
func (m Model) selectionKey() string {
	info := m.selectedProfileInfo()
	if info == nil {
		return ""
	}
	return limitsKey(m.currentProvider(), info.Name)
}

// focusKey identifies what the user is looking at: the selected provider
// and, when it has one, the selected profile. Unlike selectionKey it
// changes when moving between providers that have no accounts, so a
// message about one empty provider does not follow the user to the next.
func (m Model) focusKey() string {
	return m.currentProvider() + "\x00" + m.selectionKey()
}

// setNotice records an action outcome to show on the detail card.
func (m *Model) setNotice(provider, profile, text string, isErr bool) {
	m.notice = text
	m.noticeErr = isErr
	m.noticeKey = limitsKey(provider, profile)
}

// DefaultProviders returns the default list of provider names: every
// provider the vault knows an auth file set for.
func DefaultProviders() []string {
	return []string{"claude", "codex", "gemini", "grok", "opencode", "cursor", "agy", "kimi", "zcode"}
}

// New creates a new TUI model with default settings.
func New() Model {
	return NewWithProviders(DefaultProviders())
}

// NewWithConfig creates a new TUI model using the provided SPM config.
// This applies all TUI preferences from the config file (theme, contrast, etc.)
// with environment variable overrides already applied.
func NewWithConfig(cfg *config.SPMConfig) Model {
	return NewWithProvidersAndConfig(DefaultProviders(), cfg)
}

// NewWithProviders creates a new TUI model with the specified providers.
func NewWithProviders(providers []string) Model {
	return NewWithProvidersAndConfig(providers, nil)
}

// NewWithProvidersAndConfig creates a new TUI model with specified providers and SPM config.
// If cfg is nil, defaults are used. Otherwise, TUI preferences are loaded from cfg.
func NewWithProvidersAndConfig(providers []string, cfg *config.SPMConfig) Model {
	cwd, _ := os.Getwd()

	// Load TUI preferences from config (with env overrides) or use defaults
	var prefs TUIPreferences
	if cfg != nil {
		prefs = TUIPreferencesFromConfig(cfg)
	} else {
		prefs = LoadTUIPreferences()
	}

	// Create theme from preferences
	theme := NewTheme(prefs.ThemeOptions)

	profilesPanel := NewProfilesPanelWithTheme(theme)
	if len(providers) > 0 {
		profilesPanel.SetProvider(providers[0])
	}

	// Use runtime config from SPM config if provided
	var runtime config.RuntimeConfig
	if cfg != nil {
		runtime = cfg.Runtime
	} else {
		runtime = config.DefaultSPMConfig().Runtime
	}

	return Model{
		allProviders:    providers,
		providers:       providers,
		activeProvider:  0,
		profiles:        make(map[string][]Profile),
		selected:        0,
		state:           stateList,
		keys:            defaultKeyMap(),
		styles:          NewStyles(theme),
		stripStyles:     NewProviderPanelStyles(theme),
		profilesPanel:   profilesPanel,
		detailPanel:     NewDetailPanelWithTheme(theme),
		usagePanel:      NewUsagePanelWithTheme(theme),
		syncPanel:       NewSyncPanelWithTheme(theme),
		vaultPath:       authfile.DefaultVaultPath(),
		badges:          make(map[string]profileBadge),
		runtime:         runtime,
		cwd:             cwd,
		profileStore:    profile.NewStore(profile.DefaultStorePath()),
		profileMeta:     make(map[string]map[string]*profile.Profile),
		vaultMeta:       make(map[string]map[string]vaultProfileMeta),
		projectStore:    project.NewStore(""),
		healthStorage:   health.NewStorage(""),
		helpRenderer:    NewHelpRenderer(theme),
		theme:           theme,
		activitySpinner: NewSpinnerWithTheme(theme, ""),
	}
}

// Init implements tea.Model.
func (m Model) Init() tea.Cmd {
	cmds := []tea.Cmd{
		m.loadProfiles,
		m.loadProjectContext(),
		m.initSignals(),
	}
	if m.runtime.FileWatching {
		cmds = append(cmds, m.initWatcher())
	}
	return tea.Batch(cmds...)
}

func (m Model) loadProjectContext() tea.Cmd {
	return func() tea.Msg {
		if m.projectStore == nil || m.cwd == "" {
			return projectContextLoadedMsg{}
		}
		resolved, err := m.projectStore.Resolve(m.cwd)
		return projectContextLoadedMsg{cwd: m.cwd, resolved: resolved, err: err}
	}
}

func (m Model) initWatcher() tea.Cmd {
	return func() tea.Msg {
		w, err := watcher.New(m.vaultPath)
		return watcherReadyMsg{watcher: w, err: err}
	}
}

func (m Model) initSignals() tea.Cmd {
	return func() tea.Msg {
		h, err := signals.New()
		return signalsReadyMsg{handler: h, err: err}
	}
}

func (m Model) watchProfiles() tea.Cmd {
	if m.watcher == nil {
		return nil
	}
	return func() tea.Msg {
		select {
		case evt, ok := <-m.watcher.Events():
			if !ok {
				return nil
			}
			return profilesChangedMsg{event: evt}
		case err, ok := <-m.watcher.Errors():
			if !ok {
				return nil
			}
			return errMsg{err: err}
		}
	}
}

func (m Model) watchSignals() tea.Cmd {
	if m.signals == nil {
		return nil
	}
	return func() tea.Msg {
		select {
		case <-m.signals.Reload():
			return reloadRequestedMsg{}
		case <-m.signals.DumpStats():
			return dumpStatsMsg{}
		case sig := <-m.signals.Shutdown():
			return shutdownRequestedMsg{sig: sig}
		}
	}
}

func (m Model) loadUsageStats() tea.Cmd {
	if m.usagePanel == nil {
		return nil
	}

	days := m.usagePanel.TimeRange()
	since := time.Time{}
	if days > 0 {
		since = time.Now().UTC().Add(-time.Duration(days) * 24 * time.Hour)
	}

	return func() tea.Msg {
		db, err := caamdb.Open()
		if err != nil {
			return usageStatsLoadedMsg{err: err}
		}
		defer db.Close()

		stats, err := queryUsageStats(db, since)
		if err != nil {
			return usageStatsLoadedMsg{err: err}
		}
		return usageStatsLoadedMsg{stats: stats}
	}
}

func queryUsageStats(db *caamdb.DB, since time.Time) ([]ProfileUsage, error) {
	if db == nil || db.Conn() == nil {
		return nil, fmt.Errorf("db not available")
	}

	rows, err := db.Conn().Query(
		`SELECT provider,
		        profile_name,
		        SUM(CASE WHEN event_type = ? THEN 1 ELSE 0 END) AS sessions,
		        SUM(CASE WHEN event_type = ? THEN COALESCE(duration_seconds, 0) ELSE 0 END) AS active_seconds
		   FROM activity_log
		  WHERE datetime(timestamp) >= datetime(?)
		  GROUP BY provider, profile_name
		  ORDER BY active_seconds DESC, sessions DESC, provider ASC, profile_name ASC`,
		caamdb.EventActivate,
		caamdb.EventDeactivate,
		formatSQLiteSince(since),
	)
	if err != nil {
		return nil, fmt.Errorf("query usage stats: %w", err)
	}
	defer rows.Close()

	var out []ProfileUsage
	for rows.Next() {
		var provider, profile string
		var sessions int
		var seconds int64
		if err := rows.Scan(&provider, &profile, &sessions, &seconds); err != nil {
			return nil, fmt.Errorf("scan usage stats: %w", err)
		}
		out = append(out, ProfileUsage{
			Provider:     provider,
			ProfileName:  profile,
			SessionCount: sessions,
			TotalHours:   float64(seconds) / 3600,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate usage stats: %w", err)
	}
	return out, nil
}

func formatSQLiteSince(t time.Time) string {
	if t.IsZero() {
		return "1970-01-01 00:00:00"
	}
	return t.UTC().Format("2006-01-02 15:04:05")
}

// setActivitySpinner activates the activity spinner with the given message.
// Returns a command to start the spinner animation.
func (m *Model) setActivitySpinner(message string) tea.Cmd {
	m.activityMessage = message
	if m.activitySpinner != nil {
		return m.activitySpinner.Tick()
	}
	return nil
}

// clearActivitySpinner deactivates the activity spinner.
func (m *Model) clearActivitySpinner() {
	m.activityMessage = ""
}

// loadProfiles loads profiles for all providers.
func (m Model) loadProfiles() tea.Msg {
	vault := authfile.NewVault(m.vaultPath)
	profiles := make(map[string][]Profile)
	meta := make(map[string]map[string]*profile.Profile)
	vaultMeta := make(map[string]map[string]vaultProfileMeta)

	store := m.profileStore
	if store == nil {
		store = profile.NewStore(profile.DefaultStorePath())
	}

	for _, name := range m.allProviders {
		names, err := vault.List(name)
		if err != nil {
			return errMsg{err: fmt.Errorf("list vault profiles for %s: %w", name, err)}
		}

		active := ""
		if len(names) > 0 {
			if fileSet, ok := authFileSetForProvider(name); ok {
				if ap, err := vault.ActiveProfile(fileSet); err == nil {
					active = ap
				}
			}
		}

		sort.Strings(names)
		ps := make([]Profile, 0, len(names))
		meta[name] = make(map[string]*profile.Profile)
		vaultMeta[name] = make(map[string]vaultProfileMeta)
		for _, prof := range names {
			ps = append(ps, Profile{
				Name:     prof,
				Provider: name,
				IsActive: prof == active,
			})
			if store != nil {
				if loaded, err := store.Load(name, prof); err == nil && loaded != nil {
					meta[name][prof] = loaded
				}
			}
			vaultMeta[name][prof] = loadVaultProfileMeta(vault, name, prof)
		}
		profiles[name] = ps
	}

	applyLastUsed(vaultMeta)
	return profilesLoadedMsg{profiles: profiles, meta: meta, vaultMeta: vaultMeta, health: m.computeHealthMap(profiles)}
}

// applyLastUsed stamps each vault profile with the activity log's last use
// of it. The log is optional (analytics off, no database yet): without it
// every account simply stays "never".
func applyLastUsed(vaultMeta map[string]map[string]vaultProfileMeta) {
	db, err := caamdb.Open()
	if err != nil {
		return
	}
	defer db.Close()
	used, err := db.LastUsed()
	if err != nil {
		return
	}
	for provider, profiles := range vaultMeta {
		for name, vm := range profiles {
			if ts, ok := used[provider][name]; ok {
				vm.LastUsed = ts
				profiles[name] = vm
			}
		}
	}
}

func authFileSetForProvider(provider string) (authfile.AuthFileSet, bool) {
	return authfile.GetAuthFileSet(provider)
}

// profilesLoadedMsg is sent when profiles are loaded.
type profilesLoadedMsg struct {
	profiles  map[string][]Profile
	meta      map[string]map[string]*profile.Profile
	vaultMeta map[string]map[string]vaultProfileMeta
	health    map[string]*health.ProfileHealth
}

// errMsg is sent when an error occurs.
type errMsg struct {
	err error
}

// refreshResultMsg is sent when a token refresh operation completes.
type refreshResultMsg struct {
	provider string
	profile  string
	err      error
}

// activateResultMsg is sent when a profile activation completes.
type activateResultMsg struct {
	provider string
	profile  string
	err      error
}

// toastTickMsg is sent to check for expired toasts.
type toastTickMsg struct{}

// toastTick returns a command that ticks after the toast duration.
func toastTick() tea.Cmd {
	return tea.Tick(ToastDuration, func(t time.Time) tea.Msg {
		return toastTickMsg{}
	})
}

// addToast adds a new toast notification and returns a command to schedule expiration.
func (m *Model) addToast(message string, severity StatusSeverity) tea.Cmd {
	m.toasts = append(m.toasts, NewToast(message, severity))
	return toastTick()
}

// expireToasts removes expired toasts and returns true if any remain.
func (m *Model) expireToasts() bool {
	var active []Toast
	for _, t := range m.toasts {
		if !t.IsExpired() {
			active = append(active, t)
		}
	}
	m.toasts = active
	return len(m.toasts) > 0
}

// Update implements tea.Model.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case signalsReadyMsg:
		if msg.err != nil {
			// Not fatal: leave the TUI usable even if signals are unavailable.
			m.statusMsg = "Signal handling unavailable"
			return m, nil
		}
		m.signals = msg.handler
		return m, m.watchSignals()

	case reloadRequestedMsg:
		if !m.runtime.ReloadOnSIGHUP {
			m.statusMsg = "Reload requested (ignored; runtime.reload_on_sighup=false)"
			return m, m.watchSignals()
		}

		m.statusMsg = "Reload requested"
		cmds := []tea.Cmd{m.loadProfiles, m.loadProjectContext(), m.watchSignals()}
		if m.usagePanel != nil && m.usagePanel.Visible() {
			cmds = append(cmds, m.usagePanel.SetLoading(true))
			cmds = append(cmds, m.loadUsageStats())
		}
		return m, tea.Batch(cmds...)

	case dumpStatsMsg:
		if err := signals.AppendLogLine("", m.dumpStatsLine()); err != nil {
			m.statusMsg = fmt.Sprintf("Failed to write stats: %v", err)
		} else {
			m.statusMsg = "Stats written to log"
		}
		return m, m.watchSignals()

	case shutdownRequestedMsg:
		m.statusMsg = fmt.Sprintf("Shutdown requested (%v)", msg.sig)
		return m, tea.Quit

	case projectContextLoadedMsg:
		if msg.err != nil {
			m.statusMsg = msg.err.Error()
			return m, nil
		}
		if msg.cwd != "" {
			m.cwd = msg.cwd
		}
		m.projectContext = msg.resolved
		m.syncProfilesPanel()
		return m, nil

	case watcherReadyMsg:
		if msg.err != nil {
			// Graceful degradation: keep the TUI usable without hot reload.
			m.statusMsg = "Hot reload unavailable (file watching disabled)"
			return m, nil
		}
		m.watcher = msg.watcher
		return m, m.watchProfiles()

	case profilesChangedMsg:
		if msg.event.Type == watcher.EventProfileDeleted {
			delete(m.badges, badgeKey(msg.event.Provider, msg.event.Profile))
		}

		var badgeCmds []tea.Cmd
		if msg.event.Type == watcher.EventProfileAdded {
			if m.badges == nil {
				m.badges = make(map[string]profileBadge)
			}
			key := badgeKey(msg.event.Provider, msg.event.Profile)
			expiry := time.Now().Add(badgeLifetime)
			m.badges[key] = profileBadge{
				badge:     "NEW",
				expiry:    expiry,
				fadeLevel: 0,
			}
			badgeCmds = badgeFadeCommands(key, m.theme.ReducedMotion)
		}

		m.statusMsg = fmt.Sprintf("Profile %s/%s %s", msg.event.Provider, msg.event.Profile, eventTypeVerb(msg.event.Type))
		cmds := []tea.Cmd{m.loadProfiles, m.watchProfiles()}
		if len(badgeCmds) > 0 {
			cmds = append(cmds, badgeCmds...)
		}
		return m, tea.Batch(cmds...)

	case badgeFadeMsg:
		if m.badges != nil {
			if b, ok := m.badges[msg.key]; ok {
				if msg.level > b.fadeLevel {
					b.fadeLevel = msg.level
					m.badges[msg.key] = b
				}
			}
		}
		m.syncProfilesPanel()
		return m, nil

	case badgeExpiredMsg:
		delete(m.badges, msg.key)
		m.syncProfilesPanel()
		return m, nil

	case toastTickMsg:
		if m.expireToasts() {
			return m, toastTick()
		}
		return m, nil

	case usageStatsLoadedMsg:
		if msg.err != nil {
			m.statusMsg = msg.err.Error()
			if m.usagePanel != nil {
				m.usagePanel.SetLoading(false)
			}
			return m, nil
		}
		if m.usagePanel != nil {
			m.usagePanel.SetStats(msg.stats)
		}
		return m, nil

	case syncStateLoadedMsg:
		if msg.err != nil {
			m.statusMsg = "Failed to load sync state: " + msg.err.Error()
			if m.syncPanel != nil {
				m.syncPanel.SetLoading(false)
			}
			return m, nil
		}
		if m.syncPanel != nil {
			m.syncPanel.SetState(msg.state)
		}
		return m, nil

	case syncMachineAddedMsg:
		if msg.err != nil {
			m.statusMsg = "Failed to add machine: " + msg.err.Error()
		} else {
			m.statusMsg = "Machine added: " + msg.machine.Name
		}
		return m, m.loadSyncState()

	case syncMachineUpdatedMsg:
		if msg.err != nil {
			m.statusMsg = "Failed to update machine: " + msg.err.Error()
		} else if msg.machine != nil {
			m.statusMsg = "Machine updated: " + msg.machine.Name
		} else {
			m.statusMsg = "Machine updated"
		}
		return m, m.loadSyncState()

	case syncMachineRemovedMsg:
		if msg.err != nil {
			m.statusMsg = "Failed to remove machine: " + msg.err.Error()
		} else {
			m.statusMsg = "Machine removed"
		}
		return m, m.loadSyncState()

	case syncTestResultMsg:
		if msg.err != nil {
			m.statusMsg = "Connection test failed: " + msg.err.Error()
		} else if msg.success {
			m.statusMsg = "Connection test: " + msg.message
		} else {
			m.statusMsg = "Connection test failed: " + msg.message
		}
		return m, nil

	case syncStartedMsg:
		var spinnerCmd tea.Cmd
		if m.syncPanel != nil {
			spinnerCmd = m.syncPanel.SetSyncing(true)
		}
		if msg.machineName != "" {
			m.statusMsg = "Syncing " + msg.machineName + "..."
		} else {
			m.statusMsg = "Syncing..."
		}
		return m, spinnerCmd

	case syncCompletedMsg:
		if m.syncPanel != nil {
			m.syncPanel.SetSyncing(false)
		}
		if msg.err != nil {
			m.statusMsg = "Sync failed: " + msg.err.Error()
		} else {
			name := msg.machineName
			if name == "" {
				name = "machine"
			}
			stats := msg.stats
			m.statusMsg = fmt.Sprintf(
				"Sync complete (%s): %d pushed, %d pulled, %d skipped, %d failed",
				name,
				stats.Pushed,
				stats.Pulled,
				stats.Skipped,
				stats.Failed,
			)
		}
		return m, m.loadSyncState()

	case spinner.TickMsg:
		// Forward spinner tick messages to panels with active spinners.
		var cmds []tea.Cmd
		if m.usagePanel != nil && m.usagePanel.loading && m.usagePanel.Visible() {
			_, cmd := m.usagePanel.Update(msg)
			if cmd != nil {
				cmds = append(cmds, cmd)
			}
		}
		if m.syncPanel != nil && (m.syncPanel.loading || m.syncPanel.syncing) && m.syncPanel.Visible() {
			_, cmd := m.syncPanel.Update(msg)
			if cmd != nil {
				cmds = append(cmds, cmd)
			}
		}
		// Forward to activity spinner when active (for export/import operations)
		if m.activitySpinner != nil && m.activityMessage != "" {
			var cmd tea.Cmd
			m.activitySpinner, cmd = m.activitySpinner.Update(msg)
			if cmd != nil {
				cmds = append(cmds, cmd)
			}
		}
		if len(cmds) > 0 {
			return m, tea.Batch(cmds...)
		}
		return m, nil

	case tea.KeyMsg:
		before := m.focusKey()
		beforeStatus := m.statusMsg
		model, cmd := m.handleKeyPress(msg)
		// A key may have moved the focus; fetch that profile's limits if
		// the cached ones are missing or stale, and drop the messages
		// that were about the provider or profile the focus left.
		if next, ok := model.(Model); ok {
			if next.focusKey() != before {
				if next.noticeKey != next.selectionKey() {
					next.notice = ""
					next.noticeKey = ""
				}
				// A status line the key itself just wrote stays.
				if next.statusMsg == beforeStatus {
					next.statusMsg = ""
				}
			}
			next.settleStrip()
			// Only the list itself reads limits; keys typed into search
			// or a dialog must not start fetches.
			if next.state == stateList {
				if fetch := next.limitsPrefetchCmd(); fetch != nil {
					return next, tea.Batch(cmd, fetch)
				}
			}
			return next, cmd
		}
		return model, cmd

	case limitsLoadedMsg:
		m.applyLimitsLoaded(msg)
		return m, nil

	case newAccountLoginDoneMsg:
		return m.newAccountLoggedIn(msg)

	case newAccountIdentifiedMsg:
		return m.newAccountIdentified(msg)

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.clampDialogWidths()
		m.settleStrip()
		return m, nil

	case profilesLoadedMsg:
		m.profiles = msg.profiles
		if msg.health != nil {
			m.profileHealth = msg.health
		}
		if msg.meta != nil {
			m.profileMeta = msg.meta
		} else {
			m.profileMeta = make(map[string]map[string]*profile.Profile)
		}
		if msg.vaultMeta != nil {
			m.vaultMeta = msg.vaultMeta
		} else {
			m.vaultMeta = make(map[string]map[string]vaultProfileMeta)
		}
		// Update profiles panel with current provider's profiles
		m.syncProfilesPanel()
		fetch := m.limitsPrefetchCmd()
		return m, fetch

	case profilesRefreshedMsg:
		if msg.err != nil {
			m.showError(msg.err, "Refresh profiles")
			return m, nil
		}
		m.profiles = msg.profiles
		if msg.health != nil {
			m.profileHealth = msg.health
		}
		if msg.meta != nil {
			m.profileMeta = msg.meta
		}
		if msg.vaultMeta != nil {
			m.vaultMeta = msg.vaultMeta
		}
		// Restore selection intelligently based on context
		m.restoreSelection(msg.ctx)
		// Update profiles panel with current provider's profiles
		m.syncProfilesPanel()
		fetch := m.limitsPrefetchCmd()
		return m, fetch

	case activateResultMsg:
		if msg.err != nil {
			m.showError(msg.err, "Activate")
			// The status bar is easy to miss; the detail card is where the
			// eye is, so the refusal goes there too, verbatim.
			m.setNotice(msg.provider, msg.profile, "Activate failed: "+msg.err.Error(), true)
			return m, m.addToast(m.statusMsg, StatusError)
		}
		m.showActivateSuccess(msg.provider, msg.profile)
		m.setNotice(msg.provider, msg.profile, fmt.Sprintf("Switched %s to %s", msg.provider, msg.profile), false)
		// Refresh profiles to update active state
		ctx := refreshContext{
			provider:        msg.provider,
			selectedProfile: msg.profile,
		}
		return m, m.refreshProfiles(ctx)

	case refreshResultMsg:
		if msg.err != nil {
			m.showError(msg.err, "Refresh")
			return m, nil
		}
		m.showRefreshSuccess(msg.profile, time.Time{}) // TODO: pass actual expiry time
		// The new token changes what the limits API will say: fetch again.
		delete(m.limits, limitsKey(msg.provider, msg.profile))
		ctx := refreshContext{
			provider:        msg.provider,
			selectedProfile: msg.profile,
		}
		return m, tea.Batch(m.refreshProfiles(ctx), m.limitsPrefetchCmd())

	case errMsg:
		m.err = msg.err
		m.statusMsg = msg.err.Error()
		if m.watcher != nil {
			return m, m.watchProfiles()
		}
		return m, nil

	case exportCompleteMsg:
		return m.handleExportComplete(msg)

	case exportErrorMsg:
		return m.handleExportError(msg)

	case importPreviewMsg:
		return m.handleImportPreview(msg)

	case importCompleteMsg:
		return m.handleImportComplete(msg)

	case importErrorMsg:
		return m.handleImportError(msg)
	}

	return m, nil
}

// handleKeyPress processes keyboard input.
func (m Model) handleKeyPress(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Usage panel overlay gets first crack at keys.
	if m.usagePanel != nil && m.usagePanel.Visible() {
		if msg.Type == tea.KeyEscape {
			m.usagePanel.Toggle()
			return m, nil
		}
		switch msg.String() {
		case "u":
			m.usagePanel.Toggle()
			return m, nil
		case "1":
			m.usagePanel.SetTimeRange(1)
			spinnerCmd := m.usagePanel.SetLoading(true)
			return m, tea.Batch(spinnerCmd, m.loadUsageStats())
		case "2":
			m.usagePanel.SetTimeRange(7)
			spinnerCmd := m.usagePanel.SetLoading(true)
			return m, tea.Batch(spinnerCmd, m.loadUsageStats())
		case "3":
			m.usagePanel.SetTimeRange(30)
			spinnerCmd := m.usagePanel.SetLoading(true)
			return m, tea.Batch(spinnerCmd, m.loadUsageStats())
		case "4":
			m.usagePanel.SetTimeRange(0)
			spinnerCmd := m.usagePanel.SetLoading(true)
			return m, tea.Batch(spinnerCmd, m.loadUsageStats())
		}
	}

	// Sync panel overlay gets keys when visible.
	if m.syncPanel != nil && m.syncPanel.Visible() {
		return m.handleSyncPanelKeys(msg)
	}

	// Handle state-specific key handling
	switch m.state {
	case stateConfirm:
		return m.handleConfirmKeys(msg)
	case stateSearch:
		return m.handleSearchKeys(msg)
	case stateHelp:
		// Any key returns to list
		m.state = stateList
		return m, nil
	case stateBackupDialog:
		return m.handleBackupDialogKeys(msg)
	case stateConfirmOverwrite:
		return m.handleConfirmOverwriteKeys(msg)
	case stateExportConfirm:
		return m.handleExportConfirmKeys(msg)
	case stateImportPath:
		return m.handleImportPathKeys(msg)
	case stateImportConfirm:
		return m.handleImportConfirmKeys(msg)
	case stateEditProfile:
		return m.handleEditProfileKeys(msg)
	case stateSyncAdd:
		return m.handleSyncAddKeys(msg)
	case stateSyncEdit:
		return m.handleSyncEditKeys(msg)
	case stateCommandPalette:
		return m.handleCommandPaletteKeys(msg)
	case stateProviderPicker:
		return m.handleProviderPickerKeys(msg)
	}

	// The detail card overlay: ↑/↓ keep working underneath it; anything
	// else closes it (and, for i/esc/q, does nothing more).
	if m.showDetailCard {
		switch {
		case msg.String() == "ctrl+c":
			// Quit is quit; the card does not swallow it.
		case key.Matches(msg, m.keys.Up), key.Matches(msg, m.keys.Down):
			// fall through to the list handling below
		case key.Matches(msg, m.keys.Detail), key.Matches(msg, m.keys.Cancel), key.Matches(msg, m.keys.Quit):
			m.showDetailCard = false
			return m, nil
		default:
			m.showDetailCard = false
		}
	}

	// Normal list view key handling
	switch {
	case key.Matches(msg, m.keys.Detail):
		if m.selectedProfileInfo() != nil {
			m.showDetailCard = true
		}
		return m, nil

	case key.Matches(msg, m.keys.Refresh):
		return m.handleRefresh()

	case key.Matches(msg, m.keys.NewAccount):
		return m.handleNewAccount()

	case key.Matches(msg, m.keys.Quit):
		if m.watcher != nil {
			_ = m.watcher.Close()
			m.watcher = nil
		}
		return m, tea.Quit

	case key.Matches(msg, m.keys.Help):
		m.state = stateHelp
		return m, nil

	case key.Matches(msg, m.keys.Up):
		if m.profilesPanel != nil {
			m.profilesPanel.MoveUp()
			m.selected = m.profilesPanel.GetSelected()
			if info := m.profilesPanel.GetSelectedProfile(); info != nil {
				m.selectedProfileName = info.Name
			}
		} else if m.selected > 0 {
			m.selected--
			if name := m.selectedProfileNameValue(); name != "" {
				m.selectedProfileName = name
			}
		}
		return m, nil

	case key.Matches(msg, m.keys.Down):
		if m.profilesPanel != nil {
			m.profilesPanel.MoveDown()
			m.selected = m.profilesPanel.GetSelected()
			if info := m.profilesPanel.GetSelectedProfile(); info != nil {
				m.selectedProfileName = info.Name
			}
		} else {
			profiles := m.currentProfiles()
			if m.selected < len(profiles)-1 {
				m.selected++
				if name := m.selectedProfileNameValue(); name != "" {
					m.selectedProfileName = name
				}
			}
		}
		return m, nil

	case key.Matches(msg, m.keys.Left):
		// ←/→ walk the provider strip and wrap at its ends, like tab.
		if n := len(m.providers); n > 0 {
			m.activeProvider = (m.activeProvider + n - 1) % n
			m.selected = 0
			m.selectedProfileName = ""
			m.syncProfilesPanel()
		}
		return m, nil

	case key.Matches(msg, m.keys.Right):
		if n := len(m.providers); n > 0 {
			m.activeProvider = (m.activeProvider + 1) % n
			m.selected = 0
			m.selectedProfileName = ""
			m.syncProfilesPanel()
		}
		return m, nil

	case key.Matches(msg, m.keys.Enter):
		return m.handleActivateProfile()

	case key.Matches(msg, m.keys.Tab):
		// Cycle through providers
		if n := len(m.providers); n > 0 {
			m.activeProvider = (m.activeProvider + 1) % n
			m.selected = 0
			m.selectedProfileName = ""
			m.syncProfilesPanel()
		}
		return m, nil

	case key.Matches(msg, m.keys.Delete):
		return m.handleDeleteProfile()

	case key.Matches(msg, m.keys.Open):
		return m.handleOpenInBrowser()

	case key.Matches(msg, m.keys.Edit):
		return m.handleEditProfile()

	case key.Matches(msg, m.keys.Search):
		return m.handleEnterSearchMode()

	case key.Matches(msg, m.keys.Project):
		return m.handleSetProjectAssociation()

	case key.Matches(msg, m.keys.Usage):
		if m.usagePanel == nil {
			return m, nil
		}
		m.usagePanel.Toggle()
		if m.usagePanel.Visible() {
			spinnerCmd := m.usagePanel.SetLoading(true)
			return m, tea.Batch(spinnerCmd, m.loadUsageStats())
		}
		return m, nil

	case key.Matches(msg, m.keys.Sync):
		if m.syncPanel == nil {
			return m, nil
		}
		m.syncPanel.Toggle()
		if m.syncPanel.Visible() {
			spinnerCmd := m.syncPanel.SetLoading(true)
			return m, tea.Batch(spinnerCmd, m.loadSyncState())
		}
		return m, nil

	case key.Matches(msg, m.keys.Export):
		return m.handleExportVault()

	case key.Matches(msg, m.keys.Import):
		return m.handleImportBundle()

	case key.Matches(msg, m.keys.Palette):
		return m.handleOpenCommandPalette()
	}

	return m, nil
}

// handleConfirmKeys handles keys in confirmation state.
func (m Model) handleConfirmKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.Confirm):
		return m.executeConfirmedAction()
	case key.Matches(msg, m.keys.Cancel):
		m.state = stateList
		m.pendingAction = confirmNone
		m.statusMsg = "Cancelled"
		return m, nil
	}
	return m, nil
}

// handleSearchKeys handles keys in search/filter mode.
func (m Model) handleSearchKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEscape:
		// Cancel search and restore view
		m.state = stateList
		m.searchQuery = ""
		m.statusMsg = ""
		m.syncProfilesPanel() // Restore full list
		return m, nil

	case tea.KeyEnter:
		// Accept current filter and return to list
		m.state = stateList
		if m.searchQuery != "" {
			m.statusMsg = fmt.Sprintf("Filtered by: %s", m.searchQuery)
		} else {
			m.statusMsg = ""
		}
		return m, nil

	case tea.KeyBackspace:
		// Remove last character from search query
		if len(m.searchQuery) > 0 {
			m.searchQuery = m.searchQuery[:len(m.searchQuery)-1]
			m.applySearchFilter()
		}
		return m, nil

	case tea.KeyRunes:
		// Add typed characters to search query
		m.searchQuery += string(msg.Runes)
		m.applySearchFilter()
		return m, nil
	}
	return m, nil
}

// applySearchFilter filters the profiles panel based on the search query.
func (m *Model) applySearchFilter() {
	if m.profilesPanel == nil {
		return
	}

	provider := m.currentProvider()
	profiles := m.profiles[provider]
	projectDefault := m.projectDefaultForProvider(provider)

	// Filter profiles by name (case-insensitive)
	var filtered []ProfileInfo
	query := strings.ToLower(m.searchQuery)

	for _, p := range profiles {
		info := m.buildProfileInfo(provider, p, projectDefault)
		if profileMatchesQuery(info, query) {
			filtered = append(filtered, info)
		}
	}

	m.profilesPanel.SetProfiles(filtered)
	m.selected = 0
	m.profilesPanel.SetSelected(0)
	if info := m.profilesPanel.GetSelectedProfile(); info != nil {
		m.selectedProfileName = info.Name
	} else {
		m.selectedProfileName = ""
	}
	// Note: statusMsg not set here; search bar shows match count
}

// handleActivateProfile initiates profile activation with confirmation.
// Confirmation is required because activation replaces current auth files,
// which could be lost if not backed up.
func (m Model) handleActivateProfile() (tea.Model, tea.Cmd) {
	info := m.selectedProfileInfo()
	if info == nil {
		m.statusMsg = "No profile selected"
		return m, nil
	}

	// Check if this profile is already active (no-op)
	if info.IsActive {
		m.statusMsg = fmt.Sprintf("'%s' is already active", info.Name)
		return m, nil
	}

	// A profile with no credential cannot be switched to: installing its
	// settings would leave the live login as it is while reporting success.
	if info.NoCredential {
		provider := m.currentProvider()
		msg := fmt.Sprintf("%s has no captured credential; log in with %s as that account, then run: caam backup %s %s", info.Name, provider, provider, info.Name)
		m.setNotice(provider, info.Name, msg, true)
		m.statusMsg = "Cannot activate: no credential captured for " + info.Name
		return m, m.addToast(m.statusMsg, StatusError)
	}

	// Enter confirmation state
	m.state = stateConfirm
	m.pendingAction = confirmActivate
	m.statusMsg = fmt.Sprintf("Activate '%s'? Current auth will be replaced. (y/n)", info.Name)
	return m, nil
}

// handleDeleteProfile initiates profile deletion with confirmation.
func (m Model) handleDeleteProfile() (tea.Model, tea.Cmd) {
	info := m.selectedProfileInfo()
	if info == nil {
		m.statusMsg = "No profile selected"
		return m, nil
	}
	m.state = stateConfirm
	m.pendingAction = confirmDelete
	m.statusMsg = fmt.Sprintf("Delete '%s'? (y/n)", info.Name)
	return m, nil
}

// openBackupNameDialog asks for a profile name to capture provider's live
// credential under; used when a login's identity could not be read.
func (m Model) openBackupNameDialog(provider string) (tea.Model, tea.Cmd) {
	fileSet, ok := authFileSetForProvider(provider)
	if !ok {
		m.statusMsg = fmt.Sprintf("Unknown provider: %s", provider)
		return m, nil
	}
	if !authfile.HasAuthFiles(fileSet) {
		m.statusMsg = fmt.Sprintf("No auth files found for %s - nothing to backup", provider)
		return m, nil
	}

	m.backupProvider = provider
	m.backupDialog = NewTextInputDialog(
		fmt.Sprintf("Capture %s account", providerLabel(provider)),
		"Enter profile name (alphanumeric, underscore, hyphen, or period):",
	)
	m.backupDialog.SetStyles(m.styles)
	m.backupDialog.SetPlaceholder("work-main")
	m.backupDialog.SetWidth(m.dialogWidth(50))
	m.state = stateBackupDialog
	m.statusMsg = ""
	return m, nil
}

// backupDialogProvider is the provider the open name dialog is for.
func (m Model) backupDialogProvider() string {
	if m.backupProvider != "" {
		return m.backupProvider
	}
	return m.currentProvider()
}

// handleBackupDialogKeys handles key input for the backup dialog.
func (m Model) handleBackupDialogKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.backupDialog == nil {
		m.state = stateList
		return m, nil
	}

	// Update the dialog with the key press
	var cmd tea.Cmd
	m.backupDialog, cmd = m.backupDialog.Update(msg)

	// Check dialog result
	switch m.backupDialog.Result() {
	case DialogResultSubmit:
		profileName := m.backupDialog.Value()
		return m.processBackupSubmit(profileName)

	case DialogResultCancel:
		m.backupDialog = nil
		m.state = stateList
		m.statusMsg = "Backup cancelled"
		return m, nil
	}

	return m, cmd
}

// processBackupSubmit validates the profile name and initiates backup.
func (m Model) processBackupSubmit(profileName string) (tea.Model, tea.Cmd) {
	provider := m.backupDialogProvider()

	// Validate profile name
	profileName = strings.TrimSpace(profileName)
	if profileName == "" {
		m.statusMsg = "Profile name cannot be empty"
		m.backupDialog.Reset()
		return m, nil
	}

	// Check for reserved names
	if profileName == "." || profileName == ".." {
		m.statusMsg = "Profile name cannot be '.' or '..'"
		m.backupDialog.Reset()
		return m, nil
	}

	// Only allow alphanumeric, underscore, hyphen, and period
	// This matches the vault validation in authfile.go and profile.go
	// to prevent shell injection and filesystem issues
	for _, r := range profileName {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '_' || r == '-' || r == '.') {
			m.statusMsg = "Profile name can only contain letters, numbers, underscore, hyphen, and period"
			m.backupDialog.Reset()
			return m, nil
		}
	}

	// Check if profile already exists
	vault := authfile.NewVault(m.vaultPath)
	profiles, err := vault.List(provider)
	if err != nil {
		m.statusMsg = fmt.Sprintf("Error listing profiles: %v", err)
		m.backupDialog = nil
		m.state = stateList
		return m, nil
	}

	profileExists := false
	for _, p := range profiles {
		if p == profileName {
			profileExists = true
			break
		}
	}

	if profileExists {
		// Show overwrite confirmation dialog
		m.backupDialog = nil
		m.pendingProfile = profileName
		m.confirmDialog = NewConfirmDialog(
			"Profile Exists",
			fmt.Sprintf("Profile '%s' already exists. Overwrite?", profileName),
		)
		m.confirmDialog.SetStyles(m.styles)
		m.confirmDialog.SetLabels("Overwrite", "Cancel")
		m.confirmDialog.SetWidth(m.dialogWidth(50))
		m.state = stateConfirmOverwrite
		return m, nil
	}

	// Execute backup
	return m.executeBackup(profileName)
}

// handleConfirmOverwriteKeys handles key input for the overwrite confirmation dialog.
func (m Model) handleConfirmOverwriteKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.confirmDialog == nil {
		m.state = stateList
		return m, nil
	}

	// Update the dialog with the key press
	var cmd tea.Cmd
	m.confirmDialog, cmd = m.confirmDialog.Update(msg)

	// Check dialog result
	switch m.confirmDialog.Result() {
	case DialogResultSubmit:
		if m.confirmDialog.Confirmed() {
			profileName := m.pendingProfile
			m.confirmDialog = nil
			m.pendingProfile = ""
			return m.executeBackup(profileName)
		}
		// User selected "No" - cancel overwrite
		m.confirmDialog = nil
		m.pendingProfile = ""
		m.state = stateList
		m.statusMsg = "Backup cancelled"
		return m, nil

	case DialogResultCancel:
		m.confirmDialog = nil
		m.pendingProfile = ""
		m.state = stateList
		m.statusMsg = "Backup cancelled"
		return m, nil
	}

	return m, cmd
}

// handleSyncPanelKeys handles keys when the sync panel is visible.
func (m Model) handleSyncPanelKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.syncPanel == nil {
		return m, nil
	}

	switch msg.String() {
	case "esc", "S":
		m.syncPanel.Toggle()
		return m, nil

	case "up", "k":
		m.syncPanel.MoveUp()
		return m, nil

	case "down", "j":
		m.syncPanel.MoveDown()
		return m, nil

	case "a":
		m.syncAddDialog = newSyncMachineDialog("Add Sync Machine", nil)
		m.syncAddDialog.SetStyles(m.styles)
		m.syncAddDialog.SetWidth(m.dialogWidth(m.syncAddDialog.width))
		m.state = stateSyncAdd
		m.statusMsg = ""
		return m, nil

	case "r":
		if machine := m.syncPanel.SelectedMachine(); machine != nil {
			return m, m.removeSyncMachine(machine.ID)
		}
		return m, nil

	case "e":
		if machine := m.syncPanel.SelectedMachine(); machine != nil {
			m.pendingSyncMachine = machine.ID
			m.syncEditDialog = newSyncMachineDialog("Edit Sync Machine", machine)
			m.syncEditDialog.SetStyles(m.styles)
			m.syncEditDialog.SetWidth(m.dialogWidth(m.syncEditDialog.width))
			m.state = stateSyncEdit
			m.statusMsg = ""
			return m, nil
		}
		return m, nil

	case "t":
		if machine := m.syncPanel.SelectedMachine(); machine != nil {
			m.statusMsg = "Testing connection to " + machine.Name + "..."
			return m, m.testSyncMachine(machine.ID)
		}
		return m, nil

	case "s":
		if machine := m.syncPanel.SelectedMachine(); machine != nil {
			m.statusMsg = "Syncing " + machine.Name + "..."
			spinnerCmd := m.syncPanel.SetSyncing(true)
			return m, tea.Batch(spinnerCmd, m.syncWithMachine(machine.ID))
		}
		return m, nil

	case "l":
		m.statusMsg = "View sync history via CLI: caam sync log"
		return m, nil
	}

	return m, nil
}

// executeBackup performs the actual backup operation.
func (m Model) executeBackup(profileName string) (tea.Model, tea.Cmd) {
	provider := m.backupDialogProvider()
	fileSet, ok := authFileSetForProvider(provider)
	if !ok {
		m.state = stateList
		m.statusMsg = fmt.Sprintf("Unknown provider: %s", provider)
		return m, nil
	}

	vault := authfile.NewVault(m.vaultPath)
	if err := vault.Backup(fileSet, profileName); err != nil {
		m.state = stateList
		m.statusMsg = fmt.Sprintf("Backup failed: %v", err)
		return m, nil
	}

	m.state = stateList
	m.backupProvider = ""
	m.statusMsg = fmt.Sprintf("Backed up %s auth to '%s'", provider, profileName)

	// Reload profiles to show the new backup, and select it.
	return m, m.refreshProfiles(refreshContext{provider: provider, selectedProfile: profileName})
}

// handleRefresh (r) makes the selected account's figures fresh: the limits
// on screen are re-fetched, and when the account's token has expired or
// the provider just refused it, the token is refreshed first and the
// limits follow. A refresh spends the refresh token (the families rotate),
// so it is not done on every keypress; it waits for a reason.
func (m Model) handleRefresh() (tea.Model, tea.Cmd) {
	provider := m.currentProvider()
	info := m.selectedProfileInfo()
	if info != nil && m.tokenNeedsRefresh(provider, info.Name) {
		m.statusMsg = fmt.Sprintf("Refreshing %s's token, then its limits…", info.Name)
		return m, m.doRefreshProfile(provider, info.Name)
	}
	m.limitsRefresh()
	m.statusMsg = "Refreshing limits…"
	return m, m.limitsPrefetchCmd()
}

// tokenNeedsRefresh reports whether r should refresh the account's token
// before re-fetching its limits: only for providers caam can refresh
// (Codex and Gemini; the others renew their own tokens or cannot be
// refreshed from outside), and only when the token is past its expiry or
// the last limits fetch was refused as unauthorized.
func (m Model) tokenNeedsRefresh(provider, name string) bool {
	switch provider {
	case "codex", "gemini":
	default:
		return false
	}
	if h := m.healthFor(provider, name); h != nil && !h.TokenExpiresAt.IsZero() && time.Now().After(h.TokenExpiresAt) {
		return true
	}
	if e, ok := m.limits[limitsKey(provider, name)]; ok && e.err != nil {
		msg := strings.ToLower(e.err.Error())
		return strings.Contains(msg, "unauthorized") || strings.Contains(msg, "expired")
	}
	return false
}

// doRefreshProfile returns a tea.Cmd that performs the token refresh.
func (m Model) doRefreshProfile(provider, profile string) tea.Cmd {
	return func() tea.Msg {
		vault := authfile.NewVault(m.vaultPath)

		// Get health storage for updating health data after refresh
		store := health.NewStorage("")

		// Perform the refresh
		ctx := context.Background()
		err := refresh.RefreshProfile(ctx, provider, profile, vault, store)

		return refreshResultMsg{
			provider: provider,
			profile:  profile,
			err:      err,
		}
	}
}

// doActivateProfile returns a tea.Cmd that performs the profile activation.
func (m Model) doActivateProfile(provider, profile string) tea.Cmd {
	return func() tea.Msg {
		fileSet, ok := authFileSetForProvider(provider)
		if !ok {
			return activateResultMsg{
				provider: provider,
				profile:  profile,
				err:      fmt.Errorf("unknown provider: %s", provider),
			}
		}

		vault := authfile.NewVault(m.vaultPath)
		if err := vault.Restore(fileSet, profile); err != nil {
			return activateResultMsg{
				provider: provider,
				profile:  profile,
				err:      err,
			}
		}

		return activateResultMsg{
			provider: provider,
			profile:  profile,
			err:      nil,
		}
	}
}

// handleOpenInBrowser opens the account page in browser.
func (m Model) handleOpenInBrowser() (tea.Model, tea.Cmd) {
	provider := m.currentProvider()
	url, ok := providerAccountURLs[provider]
	if !ok {
		m.statusMsg = fmt.Sprintf("No account URL for %s", provider)
		return m, nil
	}

	launcher := &browser.DefaultLauncher{}
	if err := launcher.Open(url); err != nil {
		// If browser launch fails, show the URL so user can copy it
		m.statusMsg = fmt.Sprintf("Open in browser: %s", url)
		return m, nil
	}

	m.statusMsg = fmt.Sprintf("Opened %s account page in browser", providerLabel(provider))
	return m, nil
}

// handleEditProfile opens the edit view for the selected profile.
func (m Model) handleEditProfile() (tea.Model, tea.Cmd) {
	info := m.selectedProfileInfo()
	if info == nil {
		m.statusMsg = "No profile selected"
		return m, nil
	}
	provider := m.currentProvider()

	meta := m.profileMetaFor(provider, info.Name)
	if meta == nil {
		m.statusMsg = fmt.Sprintf("Profile metadata not found. Create with: caam profile add %s %s", provider, info.Name)
		return m, nil
	}

	fields := []FieldDefinition{
		{Label: "Description", Placeholder: "Notes about this profile", Value: meta.Description, Required: false},
		{Label: "Account Label", Placeholder: "user@example.com", Value: meta.AccountLabel, Required: false},
		{Label: "Browser Command", Placeholder: "chrome / firefox", Value: meta.BrowserCommand, Required: false},
		{Label: "Browser Profile", Placeholder: "Profile 1", Value: meta.BrowserProfileDir, Required: false},
		{Label: "Browser Name", Placeholder: "Work Chrome", Value: meta.BrowserProfileName, Required: false},
	}

	m.editDialog = NewMultiFieldDialog("Edit Profile", fields)
	m.editDialog.SetStyles(m.styles)
	m.editDialog.SetWidth(m.dialogWidth(64))
	m.pendingEditProvider = provider
	m.pendingEditProfile = info.Name
	m.state = stateEditProfile
	m.statusMsg = ""
	return m, nil
}

func (m Model) handleEditProfileKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.editDialog == nil {
		m.state = stateList
		return m, nil
	}

	var cmd tea.Cmd
	m.editDialog, cmd = m.editDialog.Update(msg)

	switch m.editDialog.Result() {
	case DialogResultSubmit:
		provider := m.pendingEditProvider
		name := m.pendingEditProfile
		meta := m.profileMetaFor(provider, name)
		if meta == nil {
			m.editDialog = nil
			m.state = stateList
			m.pendingEditProvider = ""
			m.pendingEditProfile = ""
			m.statusMsg = "Profile metadata not found"
			return m, nil
		}

		values := m.editDialog.ValueMap()
		meta.Description = strings.TrimSpace(values["Description"])
		meta.AccountLabel = strings.TrimSpace(values["Account Label"])
		meta.BrowserCommand = strings.TrimSpace(values["Browser Command"])
		meta.BrowserProfileDir = strings.TrimSpace(values["Browser Profile"])
		meta.BrowserProfileName = strings.TrimSpace(values["Browser Name"])

		if err := meta.Save(); err != nil {
			m.editDialog = nil
			m.state = stateList
			m.statusMsg = fmt.Sprintf("Failed to save profile: %v", err)
			return m, nil
		}

		m.editDialog = nil
		m.state = stateList
		m.pendingEditProvider = ""
		m.pendingEditProfile = ""
		m.statusMsg = "Profile updated"
		m.syncProfilesPanel()
		m.syncDetailPanel()
		return m, nil

	case DialogResultCancel:
		m.editDialog = nil
		m.state = stateList
		m.pendingEditProvider = ""
		m.pendingEditProfile = ""
		m.statusMsg = "Edit cancelled"
		return m, nil
	}

	return m, cmd
}

type syncMachineDialogValues struct {
	Name    string
	Address string
	Port    string
	User    string
	KeyPath string
}

func syncDialogValuesFromMachine(machine *sync.Machine) syncMachineDialogValues {
	values := syncMachineDialogValues{}
	if machine == nil {
		return values
	}
	values.Name = machine.Name
	values.Address = machine.Address
	if machine.Port > 0 {
		values.Port = fmt.Sprintf("%d", machine.Port)
	}
	values.User = machine.SSHUser
	values.KeyPath = machine.SSHKeyPath
	return values
}

func syncDialogValuesFromMap(values map[string]string) syncMachineDialogValues {
	return syncMachineDialogValues{
		Name:    strings.TrimSpace(values["Name"]),
		Address: strings.TrimSpace(values["Address"]),
		Port:    strings.TrimSpace(values["Port"]),
		User:    strings.TrimSpace(values["User"]),
		KeyPath: strings.TrimSpace(values["Key Path"]),
	}
}

func newSyncMachineDialogWithValues(title string, values syncMachineDialogValues) *MultiFieldDialog {
	fields := []FieldDefinition{
		{Label: "Name", Placeholder: "work-laptop", Value: values.Name, Required: false},
		{Label: "Address", Placeholder: "192.168.1.100", Value: values.Address, Required: false},
		{Label: "Port", Placeholder: "22", Value: values.Port, Required: false},
		{Label: "User", Placeholder: "ssh user (optional)", Value: values.User, Required: false},
		{Label: "Key Path", Placeholder: "~/.ssh/id_rsa (optional)", Value: values.KeyPath, Required: false},
	}
	dialog := NewMultiFieldDialog(title, fields)
	dialog.SetWidth(64)
	return dialog
}

func newSyncMachineDialog(title string, machine *sync.Machine) *MultiFieldDialog {
	return newSyncMachineDialogWithValues(title, syncDialogValuesFromMachine(machine))
}

func (m Model) handleSyncAddKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.syncAddDialog == nil {
		m.state = stateList
		return m, nil
	}

	var cmd tea.Cmd
	m.syncAddDialog, cmd = m.syncAddDialog.Update(msg)

	switch m.syncAddDialog.Result() {
	case DialogResultSubmit:
		values := syncDialogValuesFromMap(m.syncAddDialog.ValueMap())
		if values.Name == "" || values.Address == "" {
			m.statusMsg = "Name and address are required"
			m.syncAddDialog = newSyncMachineDialogWithValues("Add Sync Machine", values)
			m.syncAddDialog.SetStyles(m.styles)
			m.syncAddDialog.SetWidth(m.dialogWidth(m.syncAddDialog.width))
			m.state = stateSyncAdd
			return m, nil
		}
		m.syncAddDialog = nil
		m.state = stateList
		m.statusMsg = "Adding machine..."
		return m, m.addSyncMachine(values.Name, values.Address, values.Port, values.User, values.KeyPath)

	case DialogResultCancel:
		m.syncAddDialog = nil
		m.state = stateList
		m.statusMsg = "Add cancelled"
		return m, nil
	}

	return m, cmd
}

func (m Model) handleSyncEditKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.syncEditDialog == nil {
		m.state = stateList
		return m, nil
	}

	var cmd tea.Cmd
	m.syncEditDialog, cmd = m.syncEditDialog.Update(msg)

	switch m.syncEditDialog.Result() {
	case DialogResultSubmit:
		values := syncDialogValuesFromMap(m.syncEditDialog.ValueMap())
		if values.Name == "" || values.Address == "" {
			m.statusMsg = "Name and address are required"
			m.syncEditDialog = newSyncMachineDialogWithValues("Edit Sync Machine", values)
			m.syncEditDialog.SetStyles(m.styles)
			m.syncEditDialog.SetWidth(m.dialogWidth(m.syncEditDialog.width))
			m.state = stateSyncEdit
			return m, nil
		}
		machineID := m.pendingSyncMachine
		m.syncEditDialog = nil
		m.state = stateList
		m.pendingSyncMachine = ""
		m.statusMsg = "Updating machine..."
		return m, m.updateSyncMachine(machineID, values.Name, values.Address, values.Port, values.User, values.KeyPath)

	case DialogResultCancel:
		m.syncEditDialog = nil
		m.state = stateList
		m.pendingSyncMachine = ""
		m.statusMsg = "Edit cancelled"
		return m, nil
	}

	return m, cmd
}

// handleEnterSearchMode enters search/filter mode.
func (m Model) handleEnterSearchMode() (tea.Model, tea.Cmd) {
	m.state = stateSearch
	m.searchQuery = ""
	m.statusMsg = "" // Search bar shows all search info
	return m, nil
}

// handleOpenCommandPalette opens the command palette overlay.
func (m Model) handleOpenCommandPalette() (tea.Model, tea.Cmd) {
	m.commandPalette = NewCommandPaletteDialog("Command Palette", DefaultCommands())
	m.commandPalette.SetStyles(m.styles)
	m.commandPalette.SetWidth(60)
	m.state = stateCommandPalette
	m.statusMsg = ""
	return m, nil
}

// handleCommandPaletteAction executes the selected command palette action.
func (m Model) handleCommandPaletteAction(action string) (tea.Model, tea.Cmd) {
	m.state = stateList
	m.commandPalette = nil

	switch action {
	case "activate":
		return m.handleActivateProfile()
	case "delete":
		return m.handleDeleteProfile()
	case "edit":
		return m.handleEditProfile()
	case "refresh":
		return m.handleRefresh()
	case "open":
		return m.handleOpenInBrowser()
	case "project":
		return m.handleSetProjectAssociation()
	case "usage":
		if m.usagePanel != nil {
			m.usagePanel.Toggle()
			if m.usagePanel.Visible() {
				spinnerCmd := m.usagePanel.SetLoading(true)
				return m, tea.Batch(spinnerCmd, m.loadUsageStats())
			}
		}
	case "sync":
		if m.syncPanel != nil {
			m.syncPanel.Toggle()
			if m.syncPanel.Visible() {
				spinnerCmd := m.syncPanel.SetLoading(true)
				return m, tea.Batch(spinnerCmd, m.loadSyncState())
			}
		}
	case "export":
		return m.handleExportVault()
	case "import":
		return m.handleImportBundle()
	case "help":
		m.state = stateHelp
	}

	return m, nil
}

// handleCommandPaletteKeys handles keys when the command palette is open.
func (m Model) handleCommandPaletteKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.commandPalette == nil {
		m.state = stateList
		return m, nil
	}

	var cmd tea.Cmd
	m.commandPalette, cmd = m.commandPalette.Update(msg)

	switch m.commandPalette.Result() {
	case DialogResultSubmit:
		if chosen := m.commandPalette.ChosenCommand(); chosen != nil {
			return m.handleCommandPaletteAction(chosen.Action)
		}
		m.state = stateList
		m.commandPalette = nil
	case DialogResultCancel:
		m.state = stateList
		m.commandPalette = nil
	}

	return m, cmd
}

func (m Model) handleSetProjectAssociation() (tea.Model, tea.Cmd) {
	provider := m.currentProvider()
	info := m.selectedProfileInfo()
	if provider == "" || info == nil {
		m.statusMsg = "No profile selected"
		return m, nil
	}

	if m.cwd == "" {
		if cwd, err := os.Getwd(); err == nil {
			m.cwd = cwd
		}
	}
	if m.cwd == "" {
		m.statusMsg = "Unable to determine current directory"
		return m, nil
	}

	if m.projectStore == nil {
		m.projectStore = project.NewStore("")
	}

	profileName := info.Name
	if err := m.projectStore.SetAssociation(m.cwd, provider, profileName); err != nil {
		m.statusMsg = err.Error()
		return m, nil
	}

	resolved, err := m.projectStore.Resolve(m.cwd)
	if err != nil {
		m.statusMsg = err.Error()
		return m, nil
	}

	m.projectContext = resolved
	m.syncProfilesPanel()
	m.statusMsg = fmt.Sprintf("Associated %s → %s", provider, profileName)
	return m, nil
}

// executeConfirmedAction executes the pending confirmed action.
func (m Model) executeConfirmedAction() (tea.Model, tea.Cmd) {
	switch m.pendingAction {
	case confirmActivate:
		info := m.selectedProfileInfo()
		if info != nil {
			provider := m.currentProvider()

			m.statusMsg = fmt.Sprintf("Activating %s...", info.Name)
			m.state = stateList
			m.pendingAction = confirmNone

			return m, m.switchCmd(provider, info.Name)
		}

	case confirmDelete:
		info := m.selectedProfileInfo()
		if info != nil {
			provider := m.currentProvider()

			// Perform the deletion via vault
			vault := authfile.NewVault(m.vaultPath)
			if err := vault.Delete(provider, info.Name); err != nil {
				m.showError(err, fmt.Sprintf("Delete %s", info.Name))
				m.state = stateList
				m.pendingAction = confirmNone
				return m, nil
			}

			m.showDeleteSuccess(info.Name)
			m.state = stateList
			m.pendingAction = confirmNone

			// Refresh profiles with context for intelligent selection restoration
			ctx := refreshContext{
				provider:       provider,
				deletedProfile: info.Name,
			}
			return m, m.refreshProfiles(ctx)
		}
	}
	m.state = stateList
	m.pendingAction = confirmNone
	return m, nil
}

// currentProfiles returns the profiles for the currently selected provider.
func (m Model) currentProfiles() []Profile {
	if m.activeProvider >= 0 && m.activeProvider < len(m.providers) {
		return m.profiles[m.providers[m.activeProvider]]
	}
	return nil
}

// currentProvider returns the name of the currently selected provider.
func (m Model) currentProvider() string {
	if m.activeProvider >= 0 && m.activeProvider < len(m.providers) {
		return m.providers[m.activeProvider]
	}
	return ""
}

func (m Model) selectedProfileInfo() *ProfileInfo {
	if m.profilesPanel != nil {
		if info := m.profilesPanel.GetSelectedProfile(); info != nil {
			return info
		}
	}
	profiles := m.currentProfiles()
	if m.selected >= 0 && m.selected < len(profiles) {
		provider := m.currentProvider()
		projectDefault := m.projectDefaultForProvider(provider)
		info := m.buildProfileInfo(provider, profiles[m.selected], projectDefault)
		return &info
	}
	return nil
}

func (m Model) selectedProfileNameValue() string {
	if info := m.selectedProfileInfo(); info != nil {
		return info.Name
	}
	if m.selectedProfileName != "" {
		return m.selectedProfileName
	}
	profiles := m.currentProfiles()
	if m.selected >= 0 && m.selected < len(profiles) {
		return profiles[m.selected].Name
	}
	return ""
}

func (m Model) profileMetaFor(provider, name string) *profile.Profile {
	if m.profileMeta == nil {
		return nil
	}
	byProvider, ok := m.profileMeta[provider]
	if !ok {
		return nil
	}
	return byProvider[name]
}

func (m Model) vaultMetaFor(provider, name string) vaultProfileMeta {
	if m.vaultMeta == nil {
		return vaultProfileMeta{}
	}
	byProvider, ok := m.vaultMeta[provider]
	if !ok {
		return vaultProfileMeta{}
	}
	return byProvider[name]
}

func loadVaultProfileMeta(vault *authfile.Vault, provider, name string) vaultProfileMeta {
	meta := vaultProfileMeta{}
	if vault == nil || provider == "" || name == "" {
		return meta
	}

	profileDir := vault.ProfilePath(provider, name)
	metaPath := filepath.Join(profileDir, "meta.json")
	if raw, err := os.ReadFile(metaPath); err == nil {
		var stored struct {
			Description string `json:"description"`
		}
		if err := json.Unmarshal(raw, &stored); err == nil {
			meta.Description = strings.TrimSpace(stored.Description)
		}
	}

	meta.Account = vaultIdentityEmail(provider, profileDir)
	meta.NoCredential = vaultProfileHasNoCredential(provider, profileDir)
	return meta
}

// vaultProfileHasNoCredential reports a vault profile none of whose
// credential files yields a token. Providers without a credential reader
// (gemini, grok, cursor) are never flagged: the vault knows no better.
func vaultProfileHasNoCredential(provider, profileDir string) bool {
	files := usage.CredentialFiles(provider)
	if len(files) == 0 {
		return false
	}
	for _, name := range files {
		path := filepath.Join(profileDir, name)
		if _, err := os.Stat(path); err != nil {
			continue
		}
		token, _, err := usage.ReadCredentials(provider, path)
		if token != "" {
			return false
		}
		if err != nil && err.Error() == usage.ErrNoOpenCodeLimitsAPI {
			return false // an OpenCode login without a Zen key is still a login
		}
	}
	return true
}

func vaultIdentityEmail(provider, profileDir string) string {
	var id *identity.Identity
	switch provider {
	case "codex":
		id, _ = identity.ExtractFromCodexAuth(filepath.Join(profileDir, "auth.json"))
	case "claude":
		id, _ = identity.ExtractFromClaudeCredentials(filepath.Join(profileDir, ".credentials.json"))
	case "gemini":
		// Migrate legacy vault filename before reading.
		_ = authfile.MigrateGeminiVaultDir(profileDir)
		id, _ = identity.ExtractFromGeminiConfig(filepath.Join(profileDir, "settings.json"))
		if id == nil {
			id, _ = identity.ExtractFromGeminiConfig(filepath.Join(profileDir, "oauth_creds.json"))
		}
	case "grok":
		id, _ = identity.ExtractFromGrokAuth(filepath.Join(profileDir, "auth.json"))
	case "opencode":
		id, _ = identity.ExtractFromGenericAuth(filepath.Join(profileDir, "auth.json"))
	case "cursor":
		id, _ = identity.ExtractFromGenericAuth(filepath.Join(profileDir, "auth.json"))
		if id == nil {
			id, _ = identity.ExtractFromGenericAuth(filepath.Join(profileDir, "settings.json"))
		}
	case "agy":
		id, _ = identity.ExtractFromAgyProfile(profileDir)
	case "kimi":
		id, _ = identity.ExtractFromKimiCredentials(filepath.Join(profileDir, "kimi-code.json"))
	case "zcode":
		id, _ = identity.ExtractFromZcodeCredentials(filepath.Join(profileDir, "credentials.json"))
	}
	if id == nil {
		return ""
	}
	return strings.TrimSpace(id.Email)
}

func profileAccountLabel(meta *profile.Profile) string {
	if meta == nil {
		return ""
	}
	if meta.AccountLabel != "" {
		return meta.AccountLabel
	}
	if meta.Identity != nil && meta.Identity.Email != "" {
		return meta.Identity.Email
	}
	return ""
}

func profileMatchesQuery(info ProfileInfo, query string) bool {
	if query == "" {
		return true
	}
	if strings.Contains(strings.ToLower(info.Name), query) {
		return true
	}
	if info.Account != "" && strings.Contains(strings.ToLower(info.Account), query) {
		return true
	}
	if info.Description != "" && strings.Contains(strings.ToLower(info.Description), query) {
		return true
	}
	return false
}

func (m Model) buildProfileInfo(provider string, p Profile, projectDefault string) ProfileInfo {
	authMode := "oauth"
	account := ""
	description := ""
	lastUsed := time.Time{}
	locked := false

	meta := m.profileMetaFor(provider, p.Name)
	if meta != nil {
		if meta.AuthMode != "" {
			authMode = meta.AuthMode
		}
		account = profileAccountLabel(meta)
		description = meta.Description
		lastUsed = meta.LastUsedAt
		locked = meta.IsLocked()
	}

	vmeta := m.vaultMetaFor(provider, p.Name)
	if lastUsed.IsZero() {
		lastUsed = vmeta.LastUsed
	}
	if account == "" {
		account = vmeta.Account
	}
	if description == "" {
		description = vmeta.Description
	}

	healthStatus := health.StatusUnknown
	errorCount := 0
	penalty := float64(0)
	var tokenExpiry time.Time

	renewable := false
	if h := m.healthFor(provider, p.Name); h != nil {
		healthStatus = health.CalculateStatus(h)
		errorCount = h.ErrorCount1h
		penalty = h.Penalty
		tokenExpiry = h.TokenExpiresAt
		renewable = h.CredentialRenewable()
	}

	return ProfileInfo{
		Name:           p.Name,
		Badge:          m.badgeFor(provider, p.Name),
		ProjectDefault: projectDefault != "" && p.Name == projectDefault,
		AuthMode:       authMode,
		LoggedIn:       true,
		Locked:         locked,
		LastUsed:       lastUsed,
		Account:        account,
		Description:    description,
		IsActive:       p.IsActive,
		HealthStatus:   healthStatus,
		TokenExpiry:    tokenExpiry,
		ErrorCount:     errorCount,
		Penalty:        penalty,
		Renewable:      renewable,
		NoCredential:   vmeta.NoCredential,
	}
}

// syncProfilesPanel syncs the profiles panel with the current provider's profiles.
// syncProviders recomputes the visible provider list — those with at least
// one captured account — and keeps the selection on the same provider when
// it is still visible. Providers without accounts are reached through n.
func (m *Model) syncProviders() {
	all := m.allProviders
	if all == nil {
		all = m.providers
	}
	current := m.currentProvider()
	visible := make([]string, 0, len(all))
	for _, id := range all {
		if len(m.profiles[id]) > 0 {
			visible = append(visible, id)
		}
	}
	m.providers = visible
	m.activeProvider = 0
	for i, id := range visible {
		if id == current {
			m.activeProvider = i
		}
	}
}

func (m *Model) syncProfilesPanel() {
	m.syncProviders()
	if m.profilesPanel == nil {
		return
	}
	provider := m.currentProvider()
	m.profilesPanel.SetProvider(provider)

	profiles := m.profiles[provider]
	projectDefault := m.projectDefaultForProvider(provider)
	infos := make([]ProfileInfo, 0, len(profiles))
	for _, p := range profiles {
		infos = append(infos, m.buildProfileInfo(provider, p, projectDefault))
	}
	m.profilesPanel.SetProfiles(infos)

	if len(infos) == 0 {
		m.selected = 0
		m.selectedProfileName = ""
		return
	}

	selectedIndex := m.selected
	if m.selectedProfileName != "" {
		if m.profilesPanel.SetSelectedByName(m.selectedProfileName) {
			selectedIndex = m.profilesPanel.GetSelected()
		} else {
			if selectedIndex < 0 {
				selectedIndex = 0
			}
			if selectedIndex >= len(infos) {
				selectedIndex = len(infos) - 1
			}
			m.profilesPanel.SetSelected(selectedIndex)
		}
	} else {
		if selectedIndex < 0 || selectedIndex >= len(infos) {
			selectedIndex = 0
		}
		m.profilesPanel.SetSelected(selectedIndex)
	}

	m.selected = selectedIndex
	if info := m.profilesPanel.GetSelectedProfile(); info != nil {
		m.selectedProfileName = info.Name
	}
}

// syncDetailPanel syncs the detail panel with the currently selected profile.
func (m Model) syncDetailPanel() {
	if m.detailPanel == nil {
		return
	}

	// Get the selected profile
	info := m.selectedProfileInfo()
	if info == nil {
		m.detailPanel.SetProfile(nil)
		return
	}

	provider := m.currentProvider()
	profileName := info.Name

	// Default values for health data
	healthStatus := health.StatusUnknown
	errorCount := 0
	penalty := float64(0)
	var tokenExpiry time.Time

	renewable := false
	if h := m.healthFor(provider, profileName); h != nil {
		healthStatus = health.CalculateStatus(h)
		errorCount = h.ErrorCount1h
		penalty = h.Penalty
		tokenExpiry = h.TokenExpiresAt
		renewable = h.CredentialRenewable()
	}

	authMode := "oauth"
	account := ""
	description := ""
	path := ""
	createdAt := time.Time{}
	lastUsedAt := time.Time{}
	browserCmd := ""
	browserProf := ""
	locked := false

	meta := m.profileMetaFor(provider, profileName)
	if meta != nil {
		if meta.AuthMode != "" {
			authMode = meta.AuthMode
		}
		account = profileAccountLabel(meta)
		description = meta.Description
		createdAt = meta.CreatedAt
		lastUsedAt = meta.LastUsedAt
		browserCmd = meta.BrowserCommand
		if meta.BrowserProfileName != "" {
			browserProf = meta.BrowserProfileName
		} else {
			browserProf = meta.BrowserProfileDir
		}
		path = meta.BasePath
		locked = meta.IsLocked()
	}

	vmeta := m.vaultMetaFor(provider, profileName)
	if account == "" {
		account = vmeta.Account
	}
	if description == "" {
		description = vmeta.Description
	}

	if path == "" {
		path = m.vaultPathFor(provider, profileName)
	}

	detail := &DetailInfo{
		Name:         profileName,
		Provider:     provider,
		AuthMode:     authMode,
		LoggedIn:     true,
		Locked:       locked,
		Path:         path,
		CreatedAt:    createdAt,
		LastUsedAt:   lastUsedAt,
		Account:      account,
		Description:  description,
		BrowserCmd:   browserCmd,
		BrowserProf:  browserProf,
		HealthStatus: healthStatus,
		TokenExpiry:  tokenExpiry,
		ErrorCount:   errorCount,
		Penalty:      penalty,
		Limits:       m.limitsInfoFor(provider, profileName),
		Renewable:    renewable,
		NoCredential: vmeta.NoCredential,
	}
	if m.notice != "" && m.noticeKey == limitsKey(provider, profileName) {
		detail.Notice = m.notice
		detail.NoticeErr = m.noticeErr
	}
	m.detailPanel.SetProfile(detail)
}

// View implements tea.Model.
func (m Model) View() string {
	if m.width == 0 {
		return "Loading..."
	}

	switch m.state {
	case stateHelp:
		return m.helpView()
	case stateBackupDialog:
		return m.dialogOverlayView(m.backupDialog.View())
	case stateProviderPicker:
		if m.providerPicker != nil {
			return m.dialogOverlayView(m.providerPicker.View())
		}
		return m.mainView()
	case stateConfirmOverwrite:
		return m.dialogOverlayView(m.confirmDialog.View())
	case stateExportConfirm:
		if m.confirmDialog != nil {
			return m.dialogOverlayView(m.confirmDialog.View())
		}
		return m.mainView()
	case stateImportPath:
		if m.backupDialog != nil {
			return m.dialogOverlayView(m.backupDialog.View())
		}
		return m.mainView()
	case stateImportConfirm:
		if m.confirmDialog != nil {
			return m.dialogOverlayView(m.confirmDialog.View())
		}
		return m.mainView()
	case stateEditProfile:
		if m.editDialog != nil {
			return m.dialogOverlayView(m.editDialog.View())
		}
		return m.mainView()
	case stateSyncAdd:
		if m.syncAddDialog != nil {
			return m.dialogOverlayView(m.syncAddDialog.View())
		}
		return m.mainView()
	case stateSyncEdit:
		if m.syncEditDialog != nil {
			return m.dialogOverlayView(m.syncEditDialog.View())
		}
		return m.mainView()
	case stateCommandPalette:
		if m.commandPalette != nil {
			return m.dialogOverlayView(m.commandPalette.View())
		}
		return m.mainView()
	default:
		if m.usagePanel != nil && m.usagePanel.Visible() {
			m.usagePanel.SetSize(m.width, m.height)
			return m.usagePanel.View()
		}
		if m.syncPanel != nil && m.syncPanel.Visible() {
			m.syncPanel.SetSize(m.width, m.height)
			return m.syncPanel.View()
		}
		if m.showDetailCard && m.detailPanel != nil && m.width > 0 && m.height > 0 {
			m.syncDetailPanel()
			w := min(72, m.width-4)
			if w < 30 {
				w = m.width
			}
			m.detailPanel.SetSize(w, m.height-2)
			return m.dialogOverlayView(m.detailPanel.View())
		}
		return m.mainView()
	}
}

// dialogOverlayView renders the main view with a dialog overlay centered on top.
func (m Model) dialogOverlayView(dialogContent string) string {
	if m.width <= 0 || m.height <= 0 {
		return dialogContent
	}

	mainView := m.mainView()
	background := lipgloss.Place(m.width, m.height, lipgloss.Left, lipgloss.Top, mainView)
	background = m.styles.DialogOverlay.Render(background)

	dialogWidth := lipgloss.Width(dialogContent)
	dialogHeight := lipgloss.Height(dialogContent)
	if dialogWidth < 0 {
		dialogWidth = 0
	}
	if dialogHeight < 0 {
		dialogHeight = 0
	}
	if dialogWidth > m.width {
		dialogWidth = m.width
	}
	if dialogHeight > m.height {
		dialogHeight = m.height
	}

	x := (m.width - dialogWidth) / 2
	y := (m.height - dialogHeight) / 2
	if x < 0 {
		x = 0
	}
	if y < 0 {
		y = 0
	}

	bgLines := padOverlayLines(background, m.width, m.height)
	overlayLines := padOverlayLines(dialogContent, dialogWidth, dialogHeight)

	for i := 0; i < dialogHeight; i++ {
		target := y + i
		if target < 0 || target >= len(bgLines) {
			continue
		}
		left := cutANSI(bgLines[target], 0, x)
		right := cutANSI(bgLines[target], x+dialogWidth, m.width)
		overlay := overlayLines[i]
		if ansi.StringWidth(overlay) > dialogWidth {
			overlay = cutANSI(overlay, 0, dialogWidth)
		}
		bgLines[target] = left + overlay + right
	}

	return strings.Join(bgLines, "\n")
}

func padOverlayLines(content string, width, height int) []string {
	lines := strings.Split(content, "\n")
	if len(lines) > height {
		lines = lines[:height]
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	st := lipgloss.NewStyle().Width(width)
	for i, line := range lines {
		cut := cutANSI(line, 0, width)
		lines[i] = st.Render(cut)
	}
	return lines
}

func cutANSI(s string, left, right int) string {
	if right <= left {
		return ""
	}
	if right < 0 {
		return ""
	}
	if left < 0 {
		left = 0
	}
	truncated := ansi.Truncate(s, right, "")
	if left == 0 {
		return truncated
	}
	return trimLeftANSI(truncated, left)
}

func trimLeftANSI(s string, left int) string {
	if left <= 0 {
		return s
	}
	state := ansi.NormalState
	width := 0
	var out strings.Builder

	for len(s) > 0 {
		seq, w, n, newState := ansi.DecodeSequence(s, state, nil)
		state = newState
		if n == 0 {
			break
		}
		if w == 0 {
			out.WriteString(seq)
			s = s[n:]
			continue
		}
		if width+w <= left {
			width += w
			s = s[n:]
			continue
		}
		out.WriteString(seq)
		out.WriteString(s[n:])
		return out.String()
	}
	return ""
}

// mainView renders the main list view.
func (m Model) mainView() string {
	// Header
	headerLines := []string{m.styles.Header.MarginBottom(0).Render("caam - Coding Agent Account Manager")}
	if projectLine := m.projectContextLine(); projectLine != "" {
		if m.width > 0 {
			// A long project path must not widen the whole frame past the
			// terminal: JoinVertical pads every line to the widest one.
			projectLine = truncateWithEllipsis(projectLine, m.width)
		}
		headerLines = append(headerLines, m.styles.StatusText.Render(projectLine))
	}
	header := lipgloss.JoinVertical(lipgloss.Left, headerLines...)

	// Search bar (rendered when in search mode)
	searchBar := m.renderSearchBar()
	searchBarHeight := 0
	if searchBar != "" {
		searchBarHeight = lipgloss.Height(searchBar)
	}

	headerHeight := lipgloss.Height(header)
	status := m.renderStatusBar()
	statusHeight := lipgloss.Height(status)

	// A blank line separates the header from the panels when the terminal
	// can spare it; a short one gives that row to the accounts pane.
	gap := 1
	if searchBar != "" || m.height < 16 {
		gap = 0
	}
	contentHeight := m.height - headerHeight - searchBarHeight - gap - statusHeight
	if contentHeight < 0 {
		contentHeight = 0
	}

	// Providers across the top, the selected provider's accounts below.
	panels := m.verticalPanels(contentHeight)

	// The panels get whatever is left between the header and the status
	// bar, never more: a detail card taller than a short window used to
	// push the header, the provider list and the status bar off the top of
	// the terminal.
	if contentHeight > 0 {
		panels = clampLines(panels, contentHeight)
	}

	// Combine header, search bar (if active), panels, and status
	var content string
	switch {
	case searchBar != "":
		content = lipgloss.JoinVertical(lipgloss.Left, header, searchBar, panels)
	case gap == 1:
		content = lipgloss.JoinVertical(lipgloss.Left, header, "", panels)
	default:
		content = lipgloss.JoinVertical(lipgloss.Left, header, panels)
	}

	// Add status bar at bottom
	availableHeight := m.height - lipgloss.Height(content) - 1 - statusHeight
	if availableHeight > 0 {
		content = lipgloss.JoinVertical(
			lipgloss.Left,
			content,
			lipgloss.NewStyle().Height(availableHeight).Render(""),
			status,
		)
	} else {
		content = lipgloss.JoinVertical(lipgloss.Left, content, status)
	}

	return clampLines(content, m.height)
}

// clampLines keeps at most n lines of s. Whole lines are dropped, so ANSI
// styling within the kept lines is left intact.
func clampLines(s string, n int) string {
	if n <= 0 {
		return s
	}
	lines := strings.Split(s, "\n")
	if len(lines) <= n {
		return s
	}
	return strings.Join(lines[:n], "\n")
}

// layoutDebugString names the layout in force, for CAAM_DEBUG.
func (m Model) layoutDebugString() string {
	kind := map[stripKind]string{stripTabs: "tabs", stripChips: "chips", stripCards: "cards"}[m.stripKind()]
	tier := "narrow"
	switch m.width {
	case 0:
	default:
		if m.width >= wideCols {
			tier = "wide"
		} else if m.width >= mediumCols {
			tier = "medium"
		}
	}
	return fmt.Sprintf("layout=%s strip=%s w=%d h=%d", tier, kind, m.width, m.height)
}

func (m Model) debugEnabled() bool {
	return os.Getenv("CAAM_DEBUG") != ""
}

func (m Model) dialogWidth(preferred int) int {
	if preferred <= 0 {
		preferred = dialogMinWidth
	}
	if m.width <= 0 {
		return preferred
	}
	maxWidth := m.width - dialogMargin
	if maxWidth <= 0 {
		return preferred
	}
	if maxWidth < dialogMinWidth {
		return maxWidth
	}
	if preferred > maxWidth {
		return maxWidth
	}
	return preferred
}

func (m *Model) clampDialogWidths() {
	if m.backupDialog != nil {
		m.backupDialog.SetWidth(m.dialogWidth(m.backupDialog.width))
	}
	if m.confirmDialog != nil {
		m.confirmDialog.SetWidth(m.dialogWidth(m.confirmDialog.width))
	}
	if m.editDialog != nil {
		m.editDialog.SetWidth(m.dialogWidth(m.editDialog.width))
	}
	if m.syncAddDialog != nil {
		m.syncAddDialog.SetWidth(m.dialogWidth(m.syncAddDialog.width))
	}
	if m.syncEditDialog != nil {
		m.syncEditDialog.SetWidth(m.dialogWidth(m.syncEditDialog.width))
	}
}

func (m Model) projectContextLine() string {
	if m.cwd == "" {
		return ""
	}

	provider := m.currentProvider()
	if provider == "" {
		return ""
	}

	if m.projectContext == nil {
		return fmt.Sprintf("Project: %s (no association)", m.cwd)
	}

	profile := m.projectContext.Profiles[provider]
	source := m.projectContext.Sources[provider]
	if profile == "" || source == "" || source == "<default>" {
		return fmt.Sprintf("Project: %s (no association)", m.cwd)
	}

	return fmt.Sprintf("Project: %s → %s", source, profile)
}

func (m Model) projectDefaultForProvider(provider string) string {
	if provider == "" || m.projectContext == nil {
		return ""
	}

	profile := m.projectContext.Profiles[provider]
	source := m.projectContext.Sources[provider]
	if profile == "" || source == "" || source == "<default>" {
		return ""
	}

	return profile
}

func (m Model) providerCount(provider string) int {
	if m.profiles == nil {
		return 0
	}
	return len(m.profiles[provider])
}

// renderSearchBar renders a visible search bar when in search mode.
func (m Model) renderSearchBar() string {
	if m.state != stateSearch {
		return ""
	}

	// Calculate match count for display
	provider := m.currentProvider()
	profiles := m.profiles[provider]
	projectDefault := m.projectDefaultForProvider(provider)
	query := strings.ToLower(m.searchQuery)
	matchCount := 0
	for _, p := range profiles {
		info := m.buildProfileInfo(provider, p, projectDefault)
		if profileMatchesQuery(info, query) {
			matchCount++
		}
	}

	// Build search bar content
	prompt := m.styles.SearchPrompt.Render("/")
	queryText := m.styles.SearchQuery.Render(m.searchQuery)
	cursor := m.styles.SearchCursor.Render("█")
	matchInfo := m.styles.SearchMatchInfo.Render(fmt.Sprintf(" (%d matches)", matchCount))

	// Hints for search mode
	hints := m.styles.StatusKey.Render("Enter") +
		m.styles.StatusText.Render(" accept  ") +
		m.styles.StatusKey.Render("Esc") +
		m.styles.StatusText.Render(" cancel")

	// Calculate available width for search bar content
	barWidth := m.width - 4 // Account for border padding
	if barWidth < 20 {
		barWidth = 20
	}

	// Left side: prompt + query + cursor + match info
	left := prompt + queryText + cursor + matchInfo
	leftWidth := lipgloss.Width(left)
	hintsWidth := lipgloss.Width(hints)

	// Calculate gap between left and hints
	gap := barWidth - leftWidth - hintsWidth
	if gap < 1 {
		gap = 1
	}

	content := left + strings.Repeat(" ", gap) + hints
	return m.styles.SearchBar.Width(m.width - 2).Render(content)
}

// renderStatusBar renders the bottom status bar with 3 segments:
// left (mode indicator), center (status/toast message), right (key hints).
func (m Model) renderStatusBar() string {
	if m.width <= 0 {
		return ""
	}
	contentWidth := m.width - 2
	if contentWidth < 1 {
		contentWidth = m.width
	}

	// Left segment: mode indicator
	left := m.statusModeIndicator()

	// Right segment: key hints (always visible)
	right := m.statusKeyHints()

	// Center segment: status message or toast
	center := m.statusCenterMessage()

	// Calculate widths
	leftWidth := lipgloss.Width(left)
	rightWidth := lipgloss.Width(right)
	centerWidth := lipgloss.Width(center)

	// Minimum gap between segments
	minGap := 2
	availableForCenter := contentWidth - leftWidth - rightWidth - (2 * minGap)

	// Truncate center if needed
	if center != "" && centerWidth > availableForCenter && availableForCenter > 4 {
		severity := m.statusMessageSeverity()
		truncated := truncateString(m.statusCenterText(), availableForCenter)
		center = m.styles.StatusSeverityStyle(severity).Render(truncated)
		centerWidth = lipgloss.Width(center)
	}

	// Build the line with proper spacing
	if center == "" {
		// No center message: left + gap + right
		gap := contentWidth - leftWidth - rightWidth
		if gap < 1 {
			gap = 1
		}
		line := left + strings.Repeat(" ", gap) + right
		return m.styles.StatusBar.Width(m.width).Render(line)
	}

	// With center message: left + gap + center + gap + right
	leftGap := minGap
	totalUsed := leftWidth + leftGap + centerWidth + minGap + rightWidth
	if totalUsed > contentWidth {
		// Compress gaps evenly
		leftGap = 1
	}
	rightGap := contentWidth - leftWidth - leftGap - centerWidth - rightWidth
	if rightGap < 1 {
		rightGap = 1
	}

	line := left + strings.Repeat(" ", leftGap) + center + strings.Repeat(" ", rightGap) + right
	return m.styles.StatusBar.Width(m.width).Render(line)
}

// statusModeIndicator returns the rendered mode indicator for the status bar.
func (m Model) statusModeIndicator() string {
	switch m.state {
	case stateSearch:
		return m.styles.StatusModeSearch.Render("SEARCH")
	case stateHelp:
		return m.styles.StatusModeHelp.Render("HELP")
	case stateCommandPalette:
		return m.styles.StatusModeSearch.Render("CMD")
	default:
		// Show current provider as context
		if len(m.providers) > 0 && m.activeProvider >= 0 && m.activeProvider < len(m.providers) {
			provider := strings.ToUpper(providerLabel(m.providers[m.activeProvider]))
			return m.styles.StatusModeNormal.Render(provider)
		}
		return m.styles.StatusModeNormal.Render("NORMAL")
	}
}

// statusKeyHints returns the key hints for the status bar right segment.
func (m Model) statusKeyHints() string {
	hint := func(key, action string) string {
		return m.styles.StatusText.Render("[") +
			m.styles.StatusKey.Render(key) +
			m.styles.StatusText.Render(":"+action+"]")
	}

	var hints string
	switch {
	case m.width < 70:
		hints = hint("←/→", "provider")
	case m.width < 100:
		hints = hint("←/→", "provider") + " " + hint("/", "search")
	default:
		hints = hint("←/→", "provider") + " " + hint("↑/↓", "account") + " " + hint("enter", "switch") + " " + hint("n", "new login") + " " + hint("/", "search")
	}

	if m.debugEnabled() {
		hints += "  " + m.styles.StatusText.Render(m.layoutDebugString())
	}

	return hints
}

// statusCenterText returns the raw text for the center status message.
func (m Model) statusCenterText() string {
	// Toasts take priority over statusMsg
	if len(m.toasts) > 0 {
		return m.toasts[len(m.toasts)-1].Message
	}
	// Activity spinner message takes priority over regular status
	if m.activityMessage != "" {
		return m.activityMessage
	}
	return m.statusMsg
}

// statusMessageSeverity returns the severity for the current center message.
func (m Model) statusMessageSeverity() StatusSeverity {
	if len(m.toasts) > 0 {
		return m.toasts[len(m.toasts)-1].Severity
	}
	return statusSeverityFromMessage(m.statusMsg)
}

// statusCenterMessage returns the rendered center message for the status bar.
func (m Model) statusCenterMessage() string {
	text := m.statusCenterText()
	if text == "" {
		return ""
	}

	// When activity spinner is active, show spinner with message
	if m.activityMessage != "" && m.activitySpinner != nil {
		spinnerView := m.activitySpinner.ViewWithoutMessage()
		return spinnerView + " " + m.styles.StatusSeverityStyle(StatusInfo).Render(text)
	}

	severity := m.statusMessageSeverity()
	return m.styles.StatusSeverityStyle(severity).Render(text)
}

func statusSeverityFromMessage(msg string) StatusSeverity {
	msg = strings.TrimSpace(strings.ToLower(msg))
	if msg == "" {
		return StatusInfo
	}

	errorMarkers := []string{
		"error",
		"failed",
		"cannot",
		"can't",
		"unable",
		"invalid",
		"not found",
		"denied",
		"forbidden",
		"expired",
		"corrupt",
		"locked",
	}
	for _, marker := range errorMarkers {
		if strings.Contains(msg, marker) {
			return StatusError
		}
	}

	warnMarkers := []string{
		"warning",
		"warn",
		"cancelled",
		"canceled",
		"not configured",
		"no profile",
		"no profiles",
		"no auth",
		"missing",
		"ignored",
	}
	for _, marker := range warnMarkers {
		if strings.Contains(msg, marker) {
			return StatusWarning
		}
	}

	successMarkers := []string{
		"success",
		"completed",
		"complete",
		"activated",
		"added",
		"updated",
		"deleted",
		"removed",
		"backed up",
		"refreshed",
		"exported",
		"imported",
		"saved",
		"associated",
		"synced",
		"connection test:",
	}
	for _, marker := range successMarkers {
		if strings.Contains(msg, marker) {
			return StatusSuccess
		}
	}

	return StatusInfo
}

// helpView renders the help screen with Glamour markdown rendering.
func (m Model) helpView() string {
	if m.helpRenderer == nil {
		// Fallback to plain text if renderer not initialized
		return m.styles.Help.Render(MainHelpMarkdown())
	}

	// Update renderer width for proper word wrap
	contentWidth := m.width - 8 // Account for padding
	if contentWidth < 60 {
		contentWidth = 60
	}
	m.helpRenderer.SetWidth(contentWidth)

	rendered := m.helpRenderer.Render(MainHelpMarkdown())
	return m.styles.Help.Render(rendered)
}

func (m Model) dumpStatsLine() string {
	totalProfiles := 0
	for _, ps := range m.profiles {
		totalProfiles += len(ps)
	}

	activeProvider := ""
	if m.activeProvider >= 0 && m.activeProvider < len(m.providers) {
		activeProvider = m.providers[m.activeProvider]
	}

	usageVisible := false
	if m.usagePanel != nil {
		usageVisible = m.usagePanel.Visible()
	}

	return fmt.Sprintf(
		"tui_stats provider=%s selected=%d total_profiles=%d view_state=%d width=%d height=%d cwd=%q usage_visible=%t",
		activeProvider,
		m.selected,
		totalProfiles,
		m.state,
		m.width,
		m.height,
		m.cwd,
		usageVisible,
	)
}

// Run starts the TUI application.
func Run() error {
	return RunWithHooks(Hooks{})
}

// RunWithHooks runs the TUI with the command layer's switch and limits
// operations plugged in (see Hooks).
func RunWithHooks(hooks Hooks) error {
	spmCfg, err := config.LoadSPMConfig()
	if err != nil {
		// Keep the TUI usable even with a broken config file.
		spmCfg = config.DefaultSPMConfig()
	}

	// Run cleanup on startup if configured
	if spmCfg.Analytics.CleanupOnStartup {
		runStartupCleanup(spmCfg)
	}

	// Log resolved TUI config for debugging (no sensitive data to redact)
	prefs := TUIPreferencesFromConfig(spmCfg)
	slog.Debug("resolved TUI config",
		slog.String("theme", string(prefs.Mode)),
		slog.String("contrast", string(prefs.Contrast)),
		slog.Bool("no_color", prefs.NoColor),
		slog.Bool("reduced_motion", prefs.ReducedMotion),
		slog.Bool("toasts", prefs.Toasts),
		slog.Bool("mouse", prefs.Mouse),
		slog.Bool("show_key_hints", prefs.ShowKeyHints),
		slog.String("density", prefs.Density),
		slog.Bool("no_tui", prefs.NoTUI),
	)

	m := NewWithConfig(spmCfg)
	m.hooks = hooks

	pidPath := signals.DefaultPIDFilePath()
	pidWritten := false
	if spmCfg.Runtime.PIDFile {
		// Create PID file directly
		if err := os.MkdirAll(filepath.Dir(pidPath), 0700); err != nil {
			return fmt.Errorf("create pid dir: %w", err)
		}
		if err := os.WriteFile(pidPath, []byte(fmt.Sprintf("%d\n", os.Getpid())), 0600); err != nil {
			return fmt.Errorf("write pid file: %w", err)
		}
		pidWritten = true
	}

	p := tea.NewProgram(m, tea.WithAltScreen())
	finalModel, err := p.Run()

	if fm, ok := finalModel.(Model); ok {
		if fm.watcher != nil {
			_ = fm.watcher.Close()
		}
		if fm.signals != nil {
			_ = fm.signals.Close()
		}
	}
	if pidWritten {
		_ = signals.RemovePIDFile(pidPath)
	}
	return err
}

// runStartupCleanup runs database cleanup using the configured retention settings.
// Errors are silently ignored to avoid blocking TUI startup.
func runStartupCleanup(spmCfg *config.SPMConfig) {
	db, err := caamdb.Open()
	if err != nil {
		return
	}
	defer db.Close()

	cfg := caamdb.CleanupConfig{
		RetentionDays:          spmCfg.Analytics.RetentionDays,
		AggregateRetentionDays: spmCfg.Analytics.AggregateRetentionDays,
	}
	_, _ = db.Cleanup(cfg)
}

type profileBadge struct {
	badge     string
	expiry    time.Time
	fadeLevel int
}

func badgeKey(provider, profile string) string {
	return provider + "/" + profile
}

func (m Model) badgeFor(provider, profile string) string {
	if m.badges == nil {
		return ""
	}
	key := badgeKey(provider, profile)
	b, ok := m.badges[key]
	if !ok {
		return ""
	}
	if !b.expiry.IsZero() && time.Now().After(b.expiry) {
		return ""
	}
	return renderBadge(m.theme, b.badge, b.fadeLevel)
}

const (
	badgeLifetime  = 5 * time.Second
	badgeFadeSteps = 2
	badgeFadeStep  = 1 * time.Second
)

func badgeFadeCommands(key string, reducedMotion bool) []tea.Cmd {
	cmds := []tea.Cmd{}
	if !reducedMotion && badgeFadeSteps > 0 && badgeFadeStep > 0 {
		fadeStart := badgeLifetime - time.Duration(badgeFadeSteps)*badgeFadeStep
		if fadeStart < 0 {
			fadeStart = 0
		}
		for i := 1; i <= badgeFadeSteps; i++ {
			delay := fadeStart + time.Duration(i-1)*badgeFadeStep
			level := i
			if delay <= 0 {
				continue
			}
			cmds = append(cmds, tea.Tick(delay, func(time.Time) tea.Msg {
				return badgeFadeMsg{key: key, level: level}
			}))
		}
	}
	cmds = append(cmds, tea.Tick(badgeLifetime, func(time.Time) tea.Msg {
		return badgeExpiredMsg{key: key}
	}))
	return cmds
}

func renderBadge(theme Theme, label string, level int) string {
	if strings.TrimSpace(label) == "" {
		return ""
	}
	if theme.NoColor {
		if level > 0 {
			return strings.ToLower(label)
		}
		return label
	}

	style := lipgloss.NewStyle().Bold(true).Foreground(theme.Palette.Accent)
	switch {
	case level <= 0:
		// Full-intensity badge.
	case level == 1:
		style = style.Foreground(theme.Palette.Info).Faint(true)
	default:
		style = style.Foreground(theme.Palette.Muted).Faint(true)
	}
	return style.Render(label)
}

// refreshContext holds state to preserve across profile refresh operations.
type refreshContext struct {
	provider        string // Provider being modified
	selectedProfile string // Profile name that was selected before refresh
	deletedProfile  string // Profile name that was deleted (if any)
}

// profilesRefreshedMsg is sent when profiles are reloaded after a mutation.
type profilesRefreshedMsg struct {
	profiles  map[string][]Profile
	meta      map[string]map[string]*profile.Profile
	vaultMeta map[string]map[string]vaultProfileMeta
	health    map[string]*health.ProfileHealth
	ctx       refreshContext
	err       error
}

// refreshProfiles returns a tea.Cmd that reloads profiles from the vault
// while preserving selection context for intelligent index restoration.
func (m Model) refreshProfiles(ctx refreshContext) tea.Cmd {
	return func() tea.Msg {
		vault := authfile.NewVault(m.vaultPath)
		profiles := make(map[string][]Profile)
		meta := make(map[string]map[string]*profile.Profile)
		vaultMeta := make(map[string]map[string]vaultProfileMeta)

		store := m.profileStore
		if store == nil {
			store = profile.NewStore(profile.DefaultStorePath())
		}

		for _, name := range m.allProviders {
			names, err := vault.List(name)
			if err != nil {
				return profilesRefreshedMsg{
					err: fmt.Errorf("list vault profiles for %s: %w", name, err),
					ctx: ctx,
				}
			}

			active := ""
			if len(names) > 0 {
				if fileSet, ok := authFileSetForProvider(name); ok {
					if ap, err := vault.ActiveProfile(fileSet); err == nil {
						active = ap
					}
				}
			}

			sort.Strings(names)
			ps := make([]Profile, 0, len(names))
			meta[name] = make(map[string]*profile.Profile)
			vaultMeta[name] = make(map[string]vaultProfileMeta)
			for _, prof := range names {
				ps = append(ps, Profile{
					Name:     prof,
					Provider: name,
					IsActive: prof == active,
				})
				if store != nil {
					if loaded, err := store.Load(name, prof); err == nil && loaded != nil {
						meta[name][prof] = loaded
					}
				}
				vaultMeta[name][prof] = loadVaultProfileMeta(vault, name, prof)
			}
			profiles[name] = ps
		}

		applyLastUsed(vaultMeta)
		return profilesRefreshedMsg{profiles: profiles, meta: meta, vaultMeta: vaultMeta, health: m.computeHealthMap(profiles), ctx: ctx}
	}
}

// refreshProfilesSimple returns a tea.Cmd that reloads profiles preserving
// current selection by profile name.
func (m Model) refreshProfilesSimple() tea.Cmd {
	ctx := refreshContext{
		provider: m.currentProvider(),
	}
	if name := m.selectedProfileNameValue(); name != "" {
		ctx.selectedProfile = name
	}
	return m.refreshProfiles(ctx)
}

// restoreSelection finds the appropriate selection index after a refresh.
// It tries to maintain selection on the same profile, or adjusts intelligently
// if the profile was deleted.
func (m *Model) restoreSelection(ctx refreshContext) {
	profiles := m.currentProfiles()
	if len(profiles) == 0 {
		m.selected = 0
		m.selectedProfileName = ""
		return
	}

	indexByName := func(name string) int {
		for i, p := range profiles {
			if p.Name == name {
				return i
			}
		}
		return -1
	}

	// If a profile was deleted, try to select the next one in the list
	if ctx.deletedProfile != "" {
		// Find position where deleted profile was (profiles are sorted)
		for i, p := range profiles {
			if p.Name > ctx.deletedProfile {
				// Select the profile that took its place (prefer previous if possible)
				selected := i
				if selected > 0 {
					selected--
				}
				m.selectedProfileName = profiles[selected].Name
				m.selected = selected
				return
			}
		}
		// Deleted profile was last, select new last
		m.selectedProfileName = profiles[len(profiles)-1].Name
		m.selected = len(profiles) - 1
		return
	}

	// Try to find the previously selected profile by name
	if ctx.selectedProfile != "" {
		m.selectedProfileName = ctx.selectedProfile
		if idx := indexByName(ctx.selectedProfile); idx >= 0 {
			m.selected = idx
		} else {
			m.selected = 0
		}
		return
	}

	// Fallback: keep current profile name if available, otherwise derive from index
	if m.selectedProfileName == "" {
		if m.selected >= 0 && m.selected < len(profiles) {
			m.selectedProfileName = profiles[m.selected].Name
		} else {
			m.selectedProfileName = profiles[0].Name
		}
	}
	if idx := indexByName(m.selectedProfileName); idx >= 0 {
		m.selected = idx
	} else {
		m.selected = 0
		m.selectedProfileName = profiles[0].Name
	}
}

// showError sets the status message with a consistent error format.
// It maps common error types to user-friendly messages.
func (m *Model) showError(err error, context string) {
	if err == nil {
		return
	}

	msg := err.Error()

	// Map common errors to user-friendly messages
	switch {
	case strings.Contains(msg, "no such file") || strings.Contains(msg, "does not exist"):
		msg = "Profile not found in vault"
	case strings.Contains(msg, "permission denied"):
		msg = "Cannot write to auth file - check permissions"
	case strings.Contains(msg, "invalid") || strings.Contains(msg, "corrupt"):
		msg = "Profile data corrupted - try re-backup"
	case strings.Contains(msg, "already exists"):
		msg = "Profile already exists"
	case strings.Contains(msg, "locked"):
		msg = "Profile is currently locked by another process"
	}

	if context != "" {
		m.statusMsg = fmt.Sprintf("%s: %s", context, msg)
	} else {
		m.statusMsg = msg
	}
}

// showSuccess sets the status message with a success notification.
func (m *Model) showSuccess(format string, args ...interface{}) {
	m.statusMsg = fmt.Sprintf(format, args...)
}

// showActivateSuccess shows a success message for profile activation.
func (m *Model) showActivateSuccess(provider, profile string) {
	m.showSuccess("Activated %s for %s", profile, provider)
}

// showDeleteSuccess shows a success message for profile deletion.
func (m *Model) showDeleteSuccess(profile string) {
	m.showSuccess("Deleted %s", profile)
}

// showRefreshSuccess shows a success message for token refresh.
func (m *Model) showRefreshSuccess(profile string, expiresAt time.Time) {
	if expiresAt.IsZero() {
		m.showSuccess("Refreshed %s", profile)
	} else {
		m.showSuccess("Refreshed %s - new token valid until %s", profile, expiresAt.Format("Jan 2 15:04"))
	}
}

// formatError returns a user-friendly error message.
// It maps common error types to human-readable messages.
func (m Model) formatError(err error) string {
	if err == nil {
		return ""
	}

	msg := err.Error()

	// Map common errors to user-friendly messages
	switch {
	case strings.Contains(msg, "no such file") || strings.Contains(msg, "does not exist"):
		return "Profile not found in vault"
	case strings.Contains(msg, "permission denied"):
		return "Cannot write to auth file - check permissions"
	case strings.Contains(msg, "invalid") || strings.Contains(msg, "corrupt"):
		return "Profile data corrupted - try re-backup"
	case strings.Contains(msg, "already exists"):
		return "Profile already exists"
	case strings.Contains(msg, "locked"):
		return "Profile is currently locked by another process"
	}

	return msg
}

// refreshProfilesWithIndex returns a tea.Cmd that reloads profiles and
// sets the selection to the specified index after refresh.
func (m Model) refreshProfilesWithIndex(provider string, index int) tea.Cmd {
	return func() tea.Msg {
		vault := authfile.NewVault(m.vaultPath)
		profiles := make(map[string][]Profile)

		for _, name := range m.allProviders {
			names, err := vault.List(name)
			if err != nil {
				return profilesRefreshedMsg{
					err: fmt.Errorf("list vault profiles for %s: %w", name, err),
					ctx: refreshContext{provider: provider},
				}
			}

			active := ""
			if len(names) > 0 {
				if fileSet, ok := authFileSetForProvider(name); ok {
					if ap, err := vault.ActiveProfile(fileSet); err == nil {
						active = ap
					}
				}
			}

			sort.Strings(names)
			ps := make([]Profile, 0, len(names))
			for _, prof := range names {
				ps = append(ps, Profile{
					Name:     prof,
					Provider: name,
					IsActive: prof == active,
				})
			}
			profiles[name] = ps
		}

		// Create context that will set the selection index after refresh
		ctx := refreshContext{
			provider: provider,
		}

		// Set the selected profile name based on the index
		if providerProfiles := profiles[provider]; index >= 0 && index < len(providerProfiles) {
			ctx.selectedProfile = providerProfiles[index].Name
		}

		return profilesRefreshedMsg{profiles: profiles, health: m.computeHealthMap(profiles), ctx: ctx}
	}
}
