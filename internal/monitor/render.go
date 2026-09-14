package monitor

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/health"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/provider"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/usage"
)

// leftText says what is left of a profile's tightest window and when it
// resets ("12% left, resets 8:50 PM"), the sentence every human-facing
// renderer prints. ASCII only: the table pads by byte length.
func leftText(info *usage.UsageInfo, now time.Time) string {
	if info == nil {
		return "-"
	}
	return usage.WindowLeftText(info.MostConstrainedWindow(), now)
}

// displayOrderedKeys lists the state's profile keys grouped by provider in
// the dashboard strip's order (provider.DisplayOrder), then by key, so the
// table and the brief read in the order people know.
func displayOrderedKeys(state *MonitorState) []string {
	keys := sortProfileKeys(state)
	rank := make(map[string]int)
	for i, id := range provider.DisplayOrder() {
		rank[id] = i
	}
	rankOf := func(key string) int {
		if p := state.Profiles[key]; p != nil {
			if r, ok := rank[p.Provider]; ok {
				return r
			}
		}
		return len(rank)
	}
	sort.SliceStable(keys, func(i, j int) bool { return rankOf(keys[i]) < rankOf(keys[j]) })
	return keys
}

// Renderer renders monitor state into an output string.
type Renderer interface {
	Render(state *MonitorState) string
}

// RenderFormat identifies supported output formats.
type RenderFormat string

const (
	RenderTable  RenderFormat = "table"
	RenderBrief  RenderFormat = "brief"
	RenderJSON   RenderFormat = "json"
	RenderAlerts RenderFormat = "alerts"
)

// TableRenderer renders a human-friendly table.
type TableRenderer struct {
	Width     int
	ShowEmoji bool
}

// BriefRenderer renders a compact one-line summary.
type BriefRenderer struct {
	Separator string
}

// JSONRenderer renders machine-readable JSON.
type JSONRenderer struct {
	Pretty bool
}

// AlertRenderer renders alert lines and deduplicates them.
type AlertRenderer struct {
	Threshold float64
	history   map[string]AlertType
}

func newAlertRenderer(threshold float64) *AlertRenderer {
	return &AlertRenderer{
		Threshold: threshold,
		history:   make(map[string]AlertType),
	}
}

type jsonProfile struct {
	Provider      string      `json:"provider"`
	ProfileName   string      `json:"profile_name"`
	UsagePercent  float64     `json:"usage_percent,omitempty"`
	Usage         interface{} `json:"usage,omitempty"`
	Health        string      `json:"health"`
	PoolStatus    string      `json:"pool_status"`
	InCooldown    bool        `json:"in_cooldown"`
	CooldownUntil *time.Time  `json:"cooldown_until,omitempty"`
	Alert         *Alert      `json:"alert,omitempty"`
}

type jsonState struct {
	UpdatedAt time.Time     `json:"updated_at"`
	Profiles  []jsonProfile `json:"profiles"`
	Errors    []string      `json:"errors,omitempty"`
}

func formatUpdatedAt(t time.Time) string {
	if t.IsZero() {
		return "unknown"
	}
	return t.Format("15:04:05")
}

func sortProfileKeys(state *MonitorState) []string {
	if state == nil || len(state.Profiles) == 0 {
		return nil
	}
	keys := make([]string, 0, len(state.Profiles))
	for key := range state.Profiles {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func renderErrors(state *MonitorState) string {
	if state == nil || len(state.Errors) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Errors:\n")
	for _, err := range state.Errors {
		if strings.TrimSpace(err) == "" {
			continue
		}
		b.WriteString("  - ")
		b.WriteString(err)
		b.WriteString("\n")
	}
	return b.String()
}

func encodeJSONState(state *MonitorState, pretty bool) string {
	js := jsonState{
		UpdatedAt: time.Time{},
	}
	if state == nil {
		data, _ := json.Marshal(js)
		return string(data)
	}

	js.UpdatedAt = state.UpdatedAt
	if len(state.Errors) > 0 {
		js.Errors = append([]string(nil), state.Errors...)
	}

	keys := sortProfileKeys(state)
	for _, key := range keys {
		p := state.Profiles[key]
		if p == nil {
			continue
		}
		percent := usagePercent(p.Usage)
		js.Profiles = append(js.Profiles, jsonProfile{
			Provider:      p.Provider,
			ProfileName:   p.ProfileName,
			UsagePercent:  percent,
			Usage:         p.Usage,
			Health:        p.Health.String(),
			PoolStatus:    p.PoolStatus.String(),
			InCooldown:    p.InCooldown,
			CooldownUntil: p.CooldownUntil,
			Alert:         p.Alert,
		})
	}

	var data []byte
	if pretty {
		data, _ = json.MarshalIndent(js, "", "  ")
	} else {
		data, _ = json.Marshal(js)
	}
	return string(data)
}

// healthEmoji returns a short status indicator for a health status.
func healthEmoji(status health.HealthStatus) string {
	switch status {
	case health.StatusHealthy:
		return "OK"
	case health.StatusWarning:
		return "WARN"
	case health.StatusCritical:
		return "CRIT"
	default:
		return "UNK"
	}
}

// alertEmoji returns a short status indicator for an alert type.
func alertEmoji(alertType AlertType) string {
	switch alertType {
	case AlertWarning:
		return "WARN"
	case AlertCritical:
		return "CRIT"
	case AlertExhausted:
		return "EXH"
	default:
		return ""
	}
}

// progressBar creates a simple ASCII progress bar.
func progressBar(percent float64, width int) string {
	if width <= 0 {
		width = 10
	}
	filled := int(float64(width) * (percent / 100.0))
	if filled > width {
		filled = width
	}
	if filled < 0 {
		filled = 0
	}
	return strings.Repeat("#", filled) + strings.Repeat("-", width-filled)
}

// formatDuration formats a duration for display in a compact form.
func formatDuration(d time.Duration) string {
	if d < 0 {
		return "now"
	}
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	hours := int(d.Hours())
	mins := int(d.Minutes()) % 60
	if mins == 0 {
		return fmt.Sprintf("%dh", hours)
	}
	return fmt.Sprintf("%dh %dm", hours, mins)
}

// formatCooldown formats the cooldown remaining time.
func formatCooldown(until *time.Time, now time.Time) string {
	if until == nil {
		return ""
	}
	d := until.Sub(now)
	if d <= 0 {
		return "now"
	}
	return formatDuration(d)
}

// NewTableRenderer creates a TableRenderer with default settings.
func NewTableRenderer() *TableRenderer {
	return &TableRenderer{
		Width:     75,
		ShowEmoji: true,
	}
}

// NewBriefRenderer creates a BriefRenderer with default settings.
func NewBriefRenderer() *BriefRenderer {
	return &BriefRenderer{
		Separator: " ",
	}
}

// NewJSONRenderer creates a JSONRenderer with default settings.
func NewJSONRenderer(pretty bool) *JSONRenderer {
	return &JSONRenderer{
		Pretty: pretty,
	}
}

// NewAlertRenderer creates an AlertRenderer with default settings.
func NewAlertRenderer(threshold float64) *AlertRenderer {
	return newAlertRenderer(threshold)
}

// truncate shortens a string to maxLen, adding "..." if truncated.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return s[:maxLen]
	}
	return s[:maxLen-3] + "..."
}
