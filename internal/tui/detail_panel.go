package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/health"
	"github.com/charmbracelet/lipgloss"
)

// DetailInfo represents the detailed information for a profile.
type DetailInfo struct {
	Name         string
	Provider     string
	AuthMode     string
	LoggedIn     bool
	Locked       bool
	Path         string
	CreatedAt    time.Time
	LastUsedAt   time.Time
	Account      string
	Description  string // Free-form notes about this profile's purpose
	BrowserCmd   string
	BrowserProf  string
	HealthStatus health.HealthStatus
	TokenExpiry  time.Time
	ErrorCount   int
	Penalty      float64
	// Limits is the profile's live rate-limit windows; nil hides the section.
	Limits *LimitsInfo
	// NoCredential marks a profile that holds settings but no credential.
	NoCredential bool
	// Renewable marks a self-renewing credential (its expiry is not a fault).
	Renewable bool
	// Notice is the outcome of the last action on this profile (a switch
	// result, a refusal), shown under the title; NoticeErr styles it red.
	Notice    string
	NoticeErr bool
}

// LimitsInfo is the Limits section of the detail card: one row per window
// the provider reports, with the share left and the local reset time.
type LimitsInfo struct {
	Rows []LimitRow
	// AsOf is when Rows were fetched; zero while nothing has arrived.
	AsOf time.Time
	// Loading is true while a fetch is in flight (Rows may be the previous
	// result meanwhile).
	Loading bool
	// Err is the latest fetch failure. With Rows present it means Rows are
	// the last known figures (Stale); without, it is all there is to show.
	Err   string
	Stale bool
}

// LimitRow is one window: "Weekly Fable" / "10% left, resets Tue 5:00 PM".
type LimitRow struct {
	Label    string
	Value    string
	Severity string // provider's own assessment: "", "normal", "warning", "critical"
}

// DetailPanel renders the right panel showing profile details and available actions.
type DetailPanel struct {
	profile *DetailInfo
	width   int
	height  int
	styles  DetailPanelStyles
}

// DetailPanelStyles holds the styles for the detail panel.
type DetailPanelStyles struct {
	Border         lipgloss.Style
	Title          lipgloss.Style
	Label          lipgloss.Style
	Value          lipgloss.Style
	ValueNumeric   lipgloss.Style // Right-aligned numeric values
	StatusOK       lipgloss.Style
	StatusWarn     lipgloss.Style
	StatusBad      lipgloss.Style
	StatusMuted    lipgloss.Style
	LockIcon       lipgloss.Style
	Divider        lipgloss.Style
	ActionHeader   lipgloss.Style
	ActionKey      lipgloss.Style
	ActionDesc     lipgloss.Style
	Empty          lipgloss.Style
	SectionHeader  lipgloss.Style // Header for grouped sections
	SectionDivider lipgloss.Style // Subtle divider between sections
}

// DefaultDetailPanelStyles returns the default styles for the detail panel.
func DefaultDetailPanelStyles() DetailPanelStyles {
	return NewDetailPanelStyles(DefaultTheme())
}

// NewDetailPanelStyles returns themed styles for the detail panel.
func NewDetailPanelStyles(theme Theme) DetailPanelStyles {
	p := theme.Palette
	keycap := keycapStyle(theme, true).Width(8).Align(lipgloss.Center)

	return DetailPanelStyles{
		Border: lipgloss.NewStyle().
			Border(theme.Border).
			BorderForeground(p.BorderMuted).
			Background(p.Surface).
			Padding(0, 1),

		Title: lipgloss.NewStyle().
			Bold(true).
			Foreground(p.Accent).
			MarginBottom(1),

		Label: lipgloss.NewStyle().
			Foreground(p.Muted).
			Width(12),

		Value: lipgloss.NewStyle().
			Foreground(p.Text),

		ValueNumeric: lipgloss.NewStyle().
			Foreground(p.Text).
			Align(lipgloss.Right),

		StatusOK: lipgloss.NewStyle().
			Foreground(p.Success).
			Bold(true),

		StatusWarn: lipgloss.NewStyle().
			Foreground(p.Warning),

		StatusBad: lipgloss.NewStyle().
			Foreground(p.Danger),

		StatusMuted: lipgloss.NewStyle().
			Foreground(p.Muted),

		LockIcon: lipgloss.NewStyle().
			Foreground(p.Warning),

		Divider: lipgloss.NewStyle().
			Foreground(p.BorderMuted),

		ActionHeader: lipgloss.NewStyle().
			Bold(true).
			Foreground(p.Info).
			MarginTop(1).
			MarginBottom(1),

		ActionKey: keycap,

		ActionDesc: lipgloss.NewStyle().
			Foreground(p.Muted),

		Empty: lipgloss.NewStyle().
			Foreground(p.Muted).
			Italic(true).
			Padding(2, 2),

		SectionHeader: lipgloss.NewStyle().
			Bold(true).
			Foreground(p.Accent).
			MarginTop(1),

		SectionDivider: lipgloss.NewStyle().
			Foreground(p.BorderMuted).
			MarginTop(1),
	}
}

// NewDetailPanel creates a new detail panel.
func NewDetailPanel() *DetailPanel {
	return NewDetailPanelWithTheme(DefaultTheme())
}

// NewDetailPanelWithTheme creates a new detail panel using a theme.
func NewDetailPanelWithTheme(theme Theme) *DetailPanel {
	return &DetailPanel{
		styles: NewDetailPanelStyles(theme),
	}
}

// SetProfile sets the profile to display.
func (p *DetailPanel) SetProfile(profile *DetailInfo) {
	p.profile = profile
}

// SetSize sets the panel dimensions.
func (p *DetailPanel) SetSize(width, height int) {
	p.width = width
	p.height = height
}

// View renders the detail panel with grouped sections.
func (p *DetailPanel) View() string {
	if p.profile == nil {
		empty := p.styles.Empty.Render("Select a profile to view details")
		if p.width > 0 {
			return p.styles.Border.Width(p.width - 2).Render(empty)
		}
		return p.styles.Border.Render(empty)
	}

	prof := p.profile
	dividerWidth := p.width - 6
	if dividerWidth < 20 {
		dividerWidth = 20
	}
	thinDivider := p.styles.SectionDivider.Render(strings.Repeat("─", dividerWidth))

	// Title
	title := p.styles.Title.Render(fmt.Sprintf("Profile: %s", prof.Name))

	var sections []string

	// ═══ NOTICE (last action on this profile) ═══
	if prof.Notice != "" {
		width := p.width - 6
		if width < 20 {
			width = 20
		}
		style := p.styles.StatusOK
		if prof.NoticeErr {
			style = p.styles.StatusBad
		}
		sections = append(sections, style.Width(width).Render(prof.Notice))
	}

	// ═══ PROFILE SECTION ═══
	profileHeader := p.styles.SectionHeader.Render("Profile")
	var profileRows []string
	profileRows = append(profileRows, p.renderRow("Agent", providerLabel(prof.Provider)))
	if prof.Account != "" {
		profileRows = append(profileRows, p.renderRow("Account", prof.Account))
	}
	if prof.Description != "" {
		profileRows = append(profileRows, p.renderRow("Notes", prof.Description))
	}
	sections = append(sections, lipgloss.JoinVertical(lipgloss.Left,
		profileHeader,
		lipgloss.JoinVertical(lipgloss.Left, profileRows...),
	))

	// ═══ AUTH SECTION ═══
	authHeader := p.styles.SectionHeader.Render("Auth")
	var authRows []string
	authRows = append(authRows, p.renderRow("Mode", prof.AuthMode))

	// Status with icon and text
	statusText := prof.HealthStatus.Icon() + " " + prof.HealthStatus.String()
	var statusStyle lipgloss.Style
	switch prof.HealthStatus {
	case health.StatusHealthy:
		statusStyle = p.styles.StatusOK
	case health.StatusWarning:
		statusStyle = p.styles.StatusWarn
	case health.StatusCritical:
		statusStyle = p.styles.StatusBad
	default:
		statusStyle = p.styles.StatusMuted
	}
	authRows = append(authRows, p.renderRow("Status", statusStyle.Render(statusText)))

	// Token Expiry
	if !prof.TokenExpiry.IsZero() {
		ttl := time.Until(prof.TokenExpiry)
		expiryStr := ""
		switch {
		case ttl >= 0:
			expiryStr = fmt.Sprintf("Expires in %s", formatDurationFull(ttl))
		case prof.Renewable:
			expiryStr = "renews on next use"
		default:
			expiryStr = p.styles.StatusBad.Render("Expired")
		}
		authRows = append(authRows, p.renderRow("Token", expiryStr))
	}

	// Lock status
	if prof.Locked {
		authRows = append(authRows, p.renderRow("Lock", p.styles.LockIcon.Render("🔒 Locked")))
	}

	// A profile that cannot be switched to says so before Enter is pressed.
	if prof.NoCredential {
		authRows = append(authRows, p.renderRow("Credential", p.styles.StatusBad.Render(
			"none captured; press n and log in as this account")))
	}

	sections = append(sections, lipgloss.JoinVertical(lipgloss.Left,
		thinDivider,
		authHeader,
		lipgloss.JoinVertical(lipgloss.Left, authRows...),
	))

	// ═══ LIMITS SECTION ═══
	if rows := p.renderLimits(prof.Limits); len(rows) > 0 {
		sections = append(sections, lipgloss.JoinVertical(lipgloss.Left,
			thinDivider,
			p.styles.SectionHeader.Render("Limits"),
			lipgloss.JoinVertical(lipgloss.Left, rows...),
		))
	}

	// ═══ USAGE SECTION ═══
	usageHeader := p.styles.SectionHeader.Render("Usage")
	var usageRows []string

	// Errors (numeric, right-aligned conceptually but we show context)
	if prof.ErrorCount > 0 {
		errorStr := fmt.Sprintf("%d in last hour", prof.ErrorCount)
		if prof.ErrorCount >= 3 {
			errorStr = p.styles.StatusBad.Render(errorStr)
		} else {
			errorStr = p.styles.StatusWarn.Render(errorStr)
		}
		usageRows = append(usageRows, p.renderRow("Errors", errorStr))
	} else {
		usageRows = append(usageRows, p.renderRow("Errors", p.styles.StatusOK.Render("None")))
	}

	// Penalty (numeric value)
	if prof.Penalty > 0 {
		penaltyStr := fmt.Sprintf("%.2f", prof.Penalty)
		usageRows = append(usageRows, p.renderRow("Penalty", penaltyStr))
	}

	// Last used
	if !prof.LastUsedAt.IsZero() {
		usageRows = append(usageRows, p.renderRow("Last used", formatRelativeTime(prof.LastUsedAt)))
	} else {
		usageRows = append(usageRows, p.renderRow("Last used", "never"))
	}

	// Created
	if !prof.CreatedAt.IsZero() {
		usageRows = append(usageRows, p.renderRow("Created", prof.CreatedAt.Format("2006-01-02")))
	}

	sections = append(sections, lipgloss.JoinVertical(lipgloss.Left,
		thinDivider,
		usageHeader,
		lipgloss.JoinVertical(lipgloss.Left, usageRows...),
	))

	// ═══ PATHS SECTION ═══
	var pathRows []string

	// Path (truncate if too long)
	pathDisplay := prof.Path
	maxPathLen := p.width - 16
	if maxPathLen > 0 && len(pathDisplay) > maxPathLen {
		pathDisplay = "~" + pathDisplay[len(pathDisplay)-maxPathLen+1:]
	}
	if pathDisplay != "" {
		pathRows = append(pathRows, p.renderRow("Path", pathDisplay))
	}

	// Browser config
	if prof.BrowserCmd != "" || prof.BrowserProf != "" {
		browserStr := prof.BrowserCmd
		if prof.BrowserProf != "" {
			if browserStr != "" {
				browserStr += " (" + prof.BrowserProf + ")"
			} else {
				browserStr = prof.BrowserProf
			}
		}
		pathRows = append(pathRows, p.renderRow("Browser", browserStr))
	}

	if len(pathRows) > 0 {
		pathsHeader := p.styles.SectionHeader.Render("Paths")
		sections = append(sections, lipgloss.JoinVertical(lipgloss.Left,
			thinDivider,
			pathsHeader,
			lipgloss.JoinVertical(lipgloss.Left, pathRows...),
		))
	}

	// ═══ ACTIONS SECTION ═══
	divider := p.styles.Divider.Render(strings.Repeat("─", dividerWidth))
	actionsHeader := p.styles.ActionHeader.Render("Actions")

	actions := []struct {
		key  string
		desc string
	}{
		{"Enter", "Activate profile"},
		{"r", "Refresh"},
		{"e", "Edit profile"},
		{"o", "Open in browser"},
		{"d", "Delete profile"},
		{"/", "Search profiles"},
	}

	var actionRows []string
	for _, action := range actions {
		key := p.styles.ActionKey.Render(action.key)
		desc := p.styles.ActionDesc.Render(action.desc)
		actionRows = append(actionRows, fmt.Sprintf("%s %s", key, desc))
	}
	actionsContent := lipgloss.JoinVertical(lipgloss.Left, actionRows...)

	// Combine all sections
	allSections := []string{title}
	allSections = append(allSections, sections...)
	allSections = append(allSections, "", divider, actionsHeader, actionsContent)

	inner := lipgloss.JoinVertical(lipgloss.Left, allSections...)

	// Fit the panel's height: the border takes two rows, and a card that
	// runs past the bottom scrolls the whole screen (the Actions legend is
	// the least important part, so it is what gets cut).
	if p.height > 2 {
		lines := strings.Split(inner, "\n")
		if max := p.height - 2; len(lines) > max {
			inner = strings.Join(lines[:max], "\n")
		}
	}

	// Apply border
	if p.width > 0 {
		return p.styles.Border.Width(p.width - 2).Render(inner)
	}
	return p.styles.Border.Render(inner)
}

// renderLimits renders the Limits section rows, or nothing when the section
// is hidden (no fetcher wired in).
func (p *DetailPanel) renderLimits(l *LimitsInfo) []string {
	if l == nil {
		return nil
	}
	label := p.styles.Label.Width(14)
	row := func(name, value string) string {
		return label.Render(name+":") + " " + value
	}

	var rows []string
	for _, r := range l.Rows {
		value := r.Value
		switch r.Severity {
		case "critical":
			value = p.styles.StatusBad.Render(value)
		case "warning":
			value = p.styles.StatusWarn.Render(value)
		}
		rows = append(rows, row(r.Label, value))
	}

	switch {
	case len(rows) == 0 && l.Loading:
		rows = append(rows, p.styles.StatusMuted.Render("fetching..."))
	case len(rows) == 0 && l.Err != "":
		rows = append(rows, p.styles.StatusWarn.Render(shortLimitsError(l.Err)))
	case len(rows) == 0:
		rows = append(rows, p.styles.StatusMuted.Render("no windows reported"))
	case l.Stale:
		rows = append(rows, row("As of", p.styles.StatusWarn.Render(
			fmt.Sprintf("%s (last known; %s)", l.AsOf.Format("15:04:05"), shortLimitsError(l.Err)))))
	case !l.AsOf.IsZero():
		asOf := l.AsOf.Format("15:04:05")
		if l.Loading {
			asOf += " (refreshing...)"
		}
		rows = append(rows, row("As of", p.styles.StatusMuted.Render(asOf)))
	}
	return rows
}

// shortLimitsError condenses a fetch error to a phrase that fits a card
// row or a provider chip.
func shortLimitsError(err string) string {
	e := strings.ToLower(err)
	switch {
	case strings.Contains(e, "unauthorized"), strings.Contains(e, "token expired"), strings.Contains(e, "401"):
		return "auth expired (re-login)"
	case strings.Contains(e, "no usage api"), strings.Contains(e, "no limits api"):
		return "no limits API"
	case strings.Contains(e, "no z.ai coding plan"):
		return "no coding plan"
	case strings.Contains(e, "no credential"):
		return "no credential captured"
	case strings.Contains(e, "permission_denied"), strings.Contains(e, "403"):
		return "quota API refused (403)"
	}
	if i := strings.Index(err, ";"); i > 0 {
		err = err[:i]
	}
	if len(err) > 48 {
		return err[:45] + "..."
	}
	return err
}

// formatDurationFull formats duration for details view.
func formatDurationFull(d time.Duration) string {
	if d < time.Minute {
		return "less than a minute"
	}
	if d < time.Hour {
		return fmt.Sprintf("%d minutes", int(d.Minutes()))
	}
	hours := int(d.Hours())
	minutes := int(d.Minutes()) % 60
	return fmt.Sprintf("%d hours %d minutes", hours, minutes)
}

// renderRow renders a label-value row.
func (p *DetailPanel) renderRow(label, value string) string {
	labelStr := p.styles.Label.Render(label + ":")
	valueStr := p.styles.Value.Render(value)
	return labelStr + " " + valueStr
}
