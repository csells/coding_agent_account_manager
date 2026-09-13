package tui

import (
	"context"
	"errors"
	"strconv"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/health"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/usage"
)

// Hooks are the operations the TUI delegates to the command layer, which
// owns the switch path and the credential namespaces. Either may be nil.
type Hooks struct {
	// Switch makes profile the active profile for provider through the same
	// path as `caam activate` (outgoing profile re-captured first, failed
	// re-capture aborts). When nil the TUI falls back to a plain vault
	// restore.
	Switch func(ctx context.Context, provider, profile string) error
	// Limits fetches one profile's live rate-limit windows. When nil the
	// detail card shows no Limits section.
	Limits func(ctx context.Context, provider, profile string) (*usage.UsageInfo, error)
	// Health computes one profile's health verdict from its credential, the
	// way `caam ls` does. When nil the TUI reads the stored health snapshot.
	Health func(provider, profile string) *health.ProfileHealth
}

// limitsTTL is how long a fetched set of windows is shown before the
// selection landing on that profile again triggers a new fetch. Arrowing
// through the list therefore costs one request per profile per minute at
// most.
const limitsTTL = 60 * time.Second

// limitsEntry is the cached result for one provider/profile.
type limitsEntry struct {
	info    *usage.UsageInfo
	err     error
	at      time.Time
	loading bool
	// stale means info is an earlier result kept because the latest fetch
	// (err) returned no windows.
	stale bool
}

func errorString(s string) error { return errors.New(s) }

func trimFloat(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}

// limitsLoadedMsg carries a finished fetch back to Update.
type limitsLoadedMsg struct {
	provider string
	profile  string
	info     *usage.UsageInfo
	err      error
}

func limitsKey(provider, profile string) string { return provider + "/" + profile }

// limitsFetchCmd starts a fetch for the selected profile when the hook is
// set and the cached entry is missing, stale, or errored; nil otherwise.
// The model is a value, so the caller must keep the returned model: the
// loading mark lives in the (shared) map, the map itself is created here.
func (m *Model) limitsFetchCmd() tea.Cmd {
	if m.hooks.Limits == nil {
		return nil
	}
	info := m.selectedProfileInfo()
	if info == nil {
		return nil
	}
	provider, profile := m.currentProvider(), info.Name
	if provider == "" || profile == "" {
		return nil
	}
	if m.limits == nil {
		m.limits = make(map[string]limitsEntry)
	}
	key := limitsKey(provider, profile)
	e, ok := m.limits[key]
	if ok && (e.loading || (e.err == nil && time.Since(e.at) < limitsTTL)) {
		return nil
	}
	e.loading = true
	m.limits[key] = e

	fetch := m.hooks.Limits
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		res, err := fetch(ctx, provider, profile)
		return limitsLoadedMsg{provider: provider, profile: profile, info: res, err: err}
	}
}

// applyLimitsLoaded stores a fetch result. A failed fetch keeps the
// previous windows so the card can show them as last known.
func (m *Model) applyLimitsLoaded(msg limitsLoadedMsg) {
	if m.limits == nil {
		m.limits = make(map[string]limitsEntry)
	}
	key := limitsKey(msg.provider, msg.profile)
	prev := m.limits[key]
	entry := limitsEntry{info: msg.info, err: msg.err, at: time.Now()}
	if msg.err == nil && msg.info != nil && msg.info.Error != "" {
		entry.err = errorString(msg.info.Error)
	}
	if entry.err != nil && (entry.info == nil || len(usage.WindowsOf(entry.info)) == 0) && prev.info != nil {
		entry.info = prev.info
		entry.at = prev.at
		entry.stale = true
	}
	m.limits[key] = entry
}

// limitsInfoFor builds the detail card's Limits section for a profile.
func (m Model) limitsInfoFor(provider, profile string) *LimitsInfo {
	if m.hooks.Limits == nil {
		return nil
	}
	e, ok := m.limits[limitsKey(provider, profile)]
	if !ok {
		return &LimitsInfo{Loading: true}
	}
	out := &LimitsInfo{Loading: e.loading, AsOf: e.at, Stale: e.stale}
	if e.err != nil {
		out.Err = e.err.Error()
	}
	now := time.Now()
	for _, c := range usage.WindowsOf(e.info) {
		out.Rows = append(out.Rows, LimitRow{
			Label:    c.Label,
			Value:    usage.WindowLeftText(c.Window, now),
			Severity: c.Window.Severity,
		})
	}
	if e.info != nil && e.info.Credits != nil && e.info.Credits.Balance != nil {
		out.Rows = append(out.Rows, LimitRow{Label: "Credits", Value: formatCredits(e.info.Credits)})
	}
	return out
}

func formatCredits(c *usage.CreditInfo) string {
	switch {
	case c == nil:
		return "-"
	case c.Unlimited:
		return "unlimited"
	case c.Balance != nil:
		return trimFloat(*c.Balance) + " left"
	}
	return "-"
}

// switchCmd performs the confirmed activation: through the Switch hook when
// the command layer supplied one, else the TUI's own vault restore.
func (m Model) switchCmd(provider, profile string) tea.Cmd {
	if m.hooks.Switch == nil {
		return m.doActivateProfile(provider, profile)
	}
	sw := m.hooks.Switch
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		return activateResultMsg{provider: provider, profile: profile, err: sw(ctx, provider, profile)}
	}
}
