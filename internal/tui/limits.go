package tui

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
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
	// re-capture aborts). When nil the dashboard reports that switching is
	// not available.
	Switch func(ctx context.Context, provider, profile string) error
	// Limits fetches one profile's live rate-limit windows. When nil the
	// detail card shows no Limits section.
	Limits func(ctx context.Context, provider, profile string) (*usage.UsageInfo, error)
	// Health computes one profile's health verdict from its credential, the
	// way `caam ls` does. When nil the TUI reads the stored health snapshot.
	Health func(provider, profile string) *health.ProfileHealth
	// Login builds the provider's native login command for the terminal,
	// with a one-line hint for the user; an error means no login can be
	// started for that provider (not installed, no login flow). When nil
	// the `n` key says so.
	Login func(provider string) (cmd *exec.Cmd, hint string, err error)
	// LiveIdentity names the account the provider's live credential now
	// belongs to (an email), or "" when it cannot tell.
	LiveIdentity func(ctx context.Context, provider string) string
	// Capture vaults the provider's live credential under name, as
	// `caam backup` does. When nil the TUI's own vault backup is used.
	Capture func(provider, name string) error
}

// limitsTTL is how long a fetched set of windows — or a failed fetch — is
// left alone before the selection landing on that profile again triggers
// a new fetch. Arrowing through the list therefore costs one request per
// profile per minute at most, and a profile whose fetch fails (expired
// auth, a 403, a 429) is not retried on every keypress — a 429 is exactly
// the answer hammering earns.
const limitsTTL = 60 * time.Second

// limitsEntry is the cached result for one provider/profile.
type limitsEntry struct {
	info *usage.UsageInfo
	// cells is info's windows in display order (usage.WindowsOf), worked
	// out once when the entry is stored; every frame reads it.
	cells   []usage.WindowCell
	err     error
	at      time.Time
	loading bool
	// stale means info is an earlier result kept because the latest fetch
	// (err) returned no windows.
	stale bool
}

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

// limitsFetchFor starts a fetch for one profile when the cached entry is
// missing or has aged past limitsTTL; nil otherwise.
func (m *Model) limitsFetchFor(provider, profile string) tea.Cmd {
	if m.hooks.Limits == nil || provider == "" || profile == "" {
		return nil
	}
	if m.limits == nil {
		m.limits = make(map[string]limitsEntry)
	}
	key := limitsKey(provider, profile)
	e, ok := m.limits[key]
	if ok && (e.loading || time.Since(e.at) < limitsTTL) {
		return nil
	}
	e.loading = true
	m.limits[key] = e
	m.limitsGen++

	fetch := m.hooks.Limits
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		res, err := fetch(ctx, provider, profile)
		return limitsLoadedMsg{provider: provider, profile: profile, info: res, err: err}
	}
}

// limitsPrefetchCmd fetches what the screen shows: every account of the
// selected provider (the table's columns) and every provider's active
// account (the strip's summaries). Cached entries cost nothing.
func (m *Model) limitsPrefetchCmd() tea.Cmd {
	if m.hooks.Limits == nil {
		return nil
	}
	var cmds []tea.Cmd
	provider := m.currentProvider()
	for _, p := range m.profiles[provider] {
		if c := m.limitsFetchFor(provider, p.Name); c != nil {
			cmds = append(cmds, c)
		}
	}
	for _, id := range m.providers {
		for _, p := range m.profiles[id] {
			if p.IsActive {
				if c := m.limitsFetchFor(id, p.Name); c != nil {
					cmds = append(cmds, c)
				}
			}
		}
	}
	if len(cmds) == 0 {
		return nil
	}
	return tea.Batch(cmds...)
}

// limitsRefresh forgets the cached limits the screen shows so the next
// prefetch asks again.
func (m *Model) limitsRefresh() {
	provider := m.currentProvider()
	for _, p := range m.profiles[provider] {
		m.forgetLimits(provider, p.Name)
	}
	for _, id := range m.providers {
		for _, p := range m.profiles[id] {
			if p.IsActive {
				m.forgetLimits(id, p.Name)
			}
		}
	}
}

// forgetLimits drops one profile's cached limits so the next prefetch
// asks again.
func (m *Model) forgetLimits(provider, profile string) {
	delete(m.limits, limitsKey(provider, profile))
	m.limitsGen++
}

// applyLimitsLoaded stores a fetch result. A failed fetch keeps the
// previous windows so the card can show them as last known.
func (m *Model) applyLimitsLoaded(msg limitsLoadedMsg) {
	if m.limits == nil {
		m.limits = make(map[string]limitsEntry)
	}
	key := limitsKey(msg.provider, msg.profile)
	prev := m.limits[key]
	entry := limitsEntry{info: msg.info, cells: usage.WindowsOf(msg.info), err: msg.err, at: time.Now()}
	if msg.err == nil && msg.info != nil && msg.info.Error != "" {
		entry.err = errors.New(msg.info.Error)
	}
	if entry.err != nil && len(entry.cells) == 0 && prev.info != nil {
		entry.info = prev.info
		entry.cells = prev.cells
		entry.at = prev.at
		entry.stale = true
	}
	m.limits[key] = entry
	m.limitsGen++
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
	for _, c := range e.cells {
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

// switchCmd performs the confirmed activation through the Switch hook the
// command layer supplied. Without one there is no safe way to switch (a
// bare restore skips the re-capture), so the outcome says so.
func (m Model) switchCmd(provider, profile string) tea.Cmd {
	if m.hooks.Switch == nil {
		return func() tea.Msg {
			return activateResultMsg{provider: provider, profile: profile, err: fmt.Errorf("switching is not available in this build (no switch hook)")}
		}
	}
	sw := m.hooks.Switch
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		return activateResultMsg{provider: provider, profile: profile, err: sw(ctx, provider, profile)}
	}
}
