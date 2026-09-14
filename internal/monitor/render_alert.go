package monitor

import (
	"fmt"
	"strings"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/provider"
)

// Render implements the Renderer interface for AlertRenderer.
func (r *AlertRenderer) Render(state *MonitorState) string {
	if state == nil || len(state.Profiles) == 0 {
		return ""
	}

	if r.history == nil {
		r.history = make(map[string]AlertType)
	}

	threshold := r.Threshold
	if threshold <= 0 {
		threshold = warningThreshold
	}

	now := time.Now()
	lines := make([]string, 0, len(state.Profiles))

	keys := sortProfileKeys(state)
	for _, key := range keys {
		p := state.Profiles[key]
		if p == nil {
			continue
		}

		percent := usagePercent(p.Usage)
		if percent < threshold {
			delete(r.history, key)
			continue
		}

		alertLevel := AlertWarning
		switch {
		case percent >= exhaustedThreshold:
			alertLevel = AlertExhausted
		case percent >= criticalThreshold:
			alertLevel = AlertCritical
		case percent >= warningThreshold:
			alertLevel = AlertWarning
		}

		prevLevel := r.history[key]
		if alertLevel > prevLevel {
			r.history[key] = alertLevel
			tag := alertEmoji(alertLevel)
			timeStr := now.Format("2006-01-02 15:04:05")
			msg := fmt.Sprintf("[%s] %s %s %s: %s", timeStr, tag, provider.Label(p.Provider), p.ProfileName, leftText(p.Usage, now))
			msg = strings.TrimSpace(msg)
			if alertLevel == AlertExhausted {
				msg += " - SWITCH RECOMMENDED"
			}
			lines = append(lines, msg)
		}
	}

	return strings.Join(lines, "\n")
}
