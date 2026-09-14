package monitor

import (
	"strings"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/provider"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/usage"
)

// Render implements the Renderer interface for BriefRenderer: one line for
// a status bar, every provider the state holds in the strip's order, each
// by its product name with what its tightest account has left and when
// that resets. Over 80 characters the reset clocks go first, then the
// line is cut.
func (r *BriefRenderer) Render(state *MonitorState) string {
	if state == nil || len(state.Profiles) == 0 {
		return ""
	}

	sep := r.Separator
	if sep == "" {
		sep = " | "
	}

	// The tightest account per provider is the one worth a glance.
	byProvider := make(map[string]*ProfileState)
	var providers []string
	for _, key := range displayOrderedKeys(state) {
		p := state.Profiles[key]
		if p == nil {
			continue
		}
		existing, seen := byProvider[p.Provider]
		if !seen {
			providers = append(providers, p.Provider)
		}
		if existing == nil || usagePercent(p.Usage) > usagePercent(existing.Usage) {
			byProvider[p.Provider] = p
		}
	}

	now := time.Now()
	render := func(withReset bool) string {
		parts := make([]string, 0, len(providers))
		for _, prov := range providers {
			p := byProvider[prov]
			text := usage.LeftText(p.Usage.MostConstrainedWindow())
			if withReset {
				text = leftText(p.Usage, now)
			}
			parts = append(parts, provider.Label(prov)+" "+text)
		}
		return strings.Join(parts, sep)
	}

	out := render(true)
	if len(out) <= 80 {
		return out
	}
	out = render(false)
	if len(out) > 80 {
		out = out[:80]
	}
	return out
}
