package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/health"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// ProfileInfo represents a profile with all displayable information.
type ProfileInfo struct {
	Name           string
	Badge          string
	ProjectDefault bool
	AuthMode       string
	LoggedIn       bool
	Locked         bool
	LastUsed       time.Time
	Account        string
	Description    string // Free-form notes about this profile's purpose
	IsActive       bool
	HealthStatus   health.HealthStatus
	TokenExpiry    time.Time
	ErrorCount     int
	Penalty        float64
	// NoCredential marks a vault profile with settings but no credential
	// file: it cannot be switched to until it is re-captured.
	NoCredential bool
	// Renewable marks a credential the provider's CLI (or caam) renews on
	// its own, so its expiry is informational, never a fault.
	Renewable bool
}

// ProfilesPanel is the selection cursor over the selected provider's
// accounts, and the styles the accounts table draws with; the table itself
// is rendered by the vertical layout (renderAccountsPane).
type ProfilesPanel struct {
	provider string
	profiles []ProfileInfo
	selected int
	styles   ProfilesPanelStyles
}

// ProfilesPanelStyles holds the styles for the profiles panel.
type ProfilesPanelStyles struct {
	Border          lipgloss.Style
	Title           lipgloss.Style
	Header          lipgloss.Style
	Row             lipgloss.Style
	RowAlt          lipgloss.Style // Zebra stripe - alternate row background
	SelectedRow     lipgloss.Style
	ActiveIndicator lipgloss.Style
	StatusOK        lipgloss.Style
	StatusWarn      lipgloss.Style
	StatusBad       lipgloss.Style
	StatusMuted     lipgloss.Style
	LockIcon        lipgloss.Style
	ProjectBadge    lipgloss.Style
	Empty           lipgloss.Style
	// Row anatomy styles
	RowIcon         lipgloss.Style // Left icon area
	RowLabel        lipgloss.Style // Primary label (profile name)
	RowMetadata     lipgloss.Style // Secondary metadata (auth mode, last used)
	StatusBadge     lipgloss.Style // Status chip with padding
	StatusBadgeOK   lipgloss.Style
	StatusBadgeWarn lipgloss.Style
	StatusBadgeBad  lipgloss.Style
	RowSeparator    lipgloss.Style // Subtle separator between rows
}

// DefaultProfilesPanelStyles returns the default styles for the profiles panel.
func DefaultProfilesPanelStyles() ProfilesPanelStyles {
	return NewProfilesPanelStyles(DefaultTheme())
}

// NewProfilesPanelStyles returns themed styles for the profiles panel.
func NewProfilesPanelStyles(theme Theme) ProfilesPanelStyles {
	p := theme.Palette

	return ProfilesPanelStyles{
		Border: lipgloss.NewStyle().
			Border(theme.Border).
			BorderForeground(p.BorderMuted).
			Background(p.Surface).
			Padding(0, 1),

		Title: lipgloss.NewStyle().
			Bold(true).
			Foreground(p.Accent).
			MarginBottom(1),

		Header: lipgloss.NewStyle().
			Bold(true).
			Foreground(p.Muted).
			BorderStyle(lipgloss.NormalBorder()).
			BorderBottom(true).
			BorderForeground(p.BorderMuted),

		Row: lipgloss.NewStyle().
			Foreground(p.Text),

		RowAlt: lipgloss.NewStyle().
			Foreground(p.Text).
			Background(p.SurfaceMuted),

		SelectedRow: lipgloss.NewStyle().
			Foreground(p.Text).
			Bold(true).
			Background(p.Selection),

		ActiveIndicator: lipgloss.NewStyle().
			Foreground(p.Success).
			Bold(true),

		StatusOK: lipgloss.NewStyle().
			Foreground(p.Success),

		StatusWarn: lipgloss.NewStyle().
			Foreground(p.Warning),

		StatusBad: lipgloss.NewStyle().
			Foreground(p.Danger),

		StatusMuted: lipgloss.NewStyle().
			Foreground(p.Muted),

		LockIcon: lipgloss.NewStyle().
			Foreground(p.Warning),

		ProjectBadge: lipgloss.NewStyle().
			Foreground(p.Info).
			Bold(true),

		Empty: lipgloss.NewStyle().
			Foreground(p.Muted).
			Italic(true).
			Padding(2, 2),

		// Row anatomy styles
		RowIcon: lipgloss.NewStyle().
			Width(2),

		RowLabel: lipgloss.NewStyle().
			Foreground(p.Text).
			Bold(true),

		RowMetadata: lipgloss.NewStyle().
			Foreground(p.Muted),

		// Status badge styles with consistent padding and rounded appearance
		StatusBadge: lipgloss.NewStyle().
			Padding(0, 1),

		StatusBadgeOK: lipgloss.NewStyle().
			Foreground(p.Success).
			Background(p.Surface).
			Padding(0, 1).
			Bold(true),

		StatusBadgeWarn: lipgloss.NewStyle().
			Foreground(p.Warning).
			Background(p.Surface).
			Padding(0, 1).
			Bold(true),

		StatusBadgeBad: lipgloss.NewStyle().
			Foreground(p.Danger).
			Background(p.Surface).
			Padding(0, 1).
			Bold(true),

		RowSeparator: lipgloss.NewStyle().
			Foreground(p.BorderMuted),
	}
}

// StatusStyle returns the style for a given health status.
func (s ProfilesPanelStyles) StatusStyle(status health.HealthStatus) lipgloss.Style {
	switch status {
	case health.StatusHealthy:
		return s.StatusOK
	case health.StatusWarning:
		return s.StatusWarn
	case health.StatusCritical:
		return s.StatusBad
	default:
		return s.StatusMuted
	}
}

// truncateWithEllipsis truncates a string and adds ellipsis if needed.
func truncateWithEllipsis(s string, maxWidth int) string {
	if maxWidth <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= maxWidth {
		return s
	}
	if maxWidth <= 3 {
		return s[:maxWidth]
	}
	// Use runes to properly handle Unicode
	runes := []rune(s)
	for i := len(runes) - 1; i >= 0; i-- {
		candidate := string(runes[:i]) + "..."
		if lipgloss.Width(candidate) <= maxWidth {
			return candidate
		}
	}
	return "..."
}

// formatTUIStatus formats the health status string.
func formatTUIStatus(pi *ProfileInfo) string {
	icon := pi.HealthStatus.Icon()

	if pi.NoCredential {
		// Nothing to switch to: say so here, where the eye lands, not only
		// after Enter.
		return icon + " No credential"
	}

	if pi.TokenExpiry.IsZero() {
		return icon + " " + formatStatusLabel(pi.HealthStatus)
	}

	ttl := time.Until(pi.TokenExpiry)
	if ttl <= 0 {
		if pi.Renewable {
			// Matches `caam ls`: a lapsed token that renews on its own is
			// not an expired account (health.FormatStatus).
			return icon + " Auto-refresh"
		}
		return icon + " Expired"
	}

	return icon + " " + formatDuration(ttl)
}

func formatStatusLabel(status health.HealthStatus) string {
	label := status.String()
	if label == "" {
		return "Unknown"
	}
	return strings.ToUpper(label[:1]) + label[1:]
}

// formatDuration formats a duration concisely for TUI.
func formatDuration(d time.Duration) string {
	if d < time.Hour {
		return fmt.Sprintf("%dm left", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh left", int(d.Hours()))
	}
	return fmt.Sprintf("%dd left", int(d.Hours()/24))
}

// NewProfilesPanel creates a new profiles panel.
func NewProfilesPanel() *ProfilesPanel {
	return NewProfilesPanelWithTheme(DefaultTheme())
}

// NewProfilesPanelWithTheme creates a new profiles panel using a theme.
func NewProfilesPanelWithTheme(theme Theme) *ProfilesPanel {
	return &ProfilesPanel{
		profiles: []ProfileInfo{},
		styles:   NewProfilesPanelStyles(theme),
	}
}

// SetProvider sets the currently displayed provider.
func (p *ProfilesPanel) SetProvider(provider string) {
	p.provider = provider
}

// SetProfiles sets the profiles to display, sorted by last used.
func (p *ProfilesPanel) SetProfiles(profiles []ProfileInfo) {
	// Sort by last used (most recent first), then by name
	sorted := make([]ProfileInfo, len(profiles))
	copy(sorted, profiles)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].LastUsed.Equal(sorted[j].LastUsed) {
			return sorted[i].Name < sorted[j].Name
		}
		return sorted[i].LastUsed.After(sorted[j].LastUsed)
	})
	p.profiles = sorted

	// Reset selection if out of bounds
	if p.selected >= len(p.profiles) {
		p.selected = max(0, len(p.profiles)-1)
	}
}

// SetSelected sets the currently selected profile index.
func (p *ProfilesPanel) SetSelected(index int) {
	if index >= 0 && index < len(p.profiles) {
		p.selected = index
	}
}

// SetSelectedByName sets the selected profile by name.
// Returns true if the profile was found.
func (p *ProfilesPanel) SetSelectedByName(name string) bool {
	for i := range p.profiles {
		if p.profiles[i].Name == name {
			p.selected = i
			return true
		}
	}
	return false
}

// Count returns the number of profiles in the panel.
func (p *ProfilesPanel) Count() int {
	return len(p.profiles)
}

// GetSelected returns the currently selected profile index.
func (p *ProfilesPanel) GetSelected() int {
	return p.selected
}

// GetSelectedProfile returns the currently selected profile, or nil if none.
func (p *ProfilesPanel) GetSelectedProfile() *ProfileInfo {
	if p.selected >= 0 && p.selected < len(p.profiles) {
		return &p.profiles[p.selected]
	}
	return nil
}

// MoveUp moves selection up.
func (p *ProfilesPanel) MoveUp() {
	if p.selected > 0 {
		p.selected--
	}
}

// MoveDown moves selection down.
func (p *ProfilesPanel) MoveDown() {
	if p.selected < len(p.profiles)-1 {
		p.selected++
	}
}

func emptyProfilesMessage(provider string) string {
	label := providerLabel(provider)
	return fmt.Sprintf("📭 No profiles for %s yet\n\nRun: caam backup %s <email>", label, provider)
}

// formatRelativeTime formats a time as a relative string (e.g., "2h ago", "1d ago").
func formatRelativeTime(t time.Time) string {
	if t.IsZero() {
		return "never"
	}

	duration := time.Since(t)

	switch {
	case duration < time.Minute:
		return "now"
	case duration < time.Hour:
		mins := int(duration.Minutes())
		return fmt.Sprintf("%dm ago", mins)
	case duration < 24*time.Hour:
		hours := int(duration.Hours())
		return fmt.Sprintf("%dh ago", hours)
	case duration < 7*24*time.Hour:
		days := int(duration.Hours() / 24)
		return fmt.Sprintf("%dd ago", days)
	case duration < 30*24*time.Hour:
		weeks := int(duration.Hours() / (24 * 7))
		return fmt.Sprintf("%dw ago", weeks)
	default:
		months := int(duration.Hours() / (24 * 30))
		if months == 0 {
			months = 1
		}
		return fmt.Sprintf("%dmo ago", months)
	}
}

// padRight pads a string to the right with spaces.
// Uses lipgloss.Width for proper visual width handling (emojis, CJK).
func padRight(s string, width int) string {
	w := lipgloss.Width(s)
	if w >= width {
		return s
	}
	return s + strings.Repeat(" ", width-w)
}

// truncate truncates a string to the given width in runes.
// Uses rune handling for proper Unicode support.
func truncate(s string, width int) string {
	runes := []rune(s)
	if len(runes) <= width {
		return s
	}
	if width <= 3 {
		return string(runes[:width])
	}
	return string(runes[:width-3]) + "..."
}

func formatNameWithBadge(name, badge string, width int) string {
	if badge == "" {
		return truncate(name, width)
	}
	if width <= 0 {
		return ""
	}

	badgePlain := ansi.Strip(badge)
	badgeRunes := utf8.RuneCountInString(badgePlain)
	if badgeRunes >= width {
		return truncate(badgePlain, width)
	}

	nameWidth := width - 1 - badgeRunes
	if nameWidth < 0 {
		nameWidth = 0
	}

	return truncate(name, nameWidth) + " " + badge
}
