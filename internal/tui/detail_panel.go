package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/health"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/usage"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// DetailInfo is everything the dashboard knows about the selected account:
// what the detail panel under the accounts list is drawn from.
type DetailInfo struct {
	Name         string
	Provider     string
	Active       bool
	AuthMode     string
	PlanType     string
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
	// Limits is the profile's live rate-limit windows; nil hides the group.
	Limits *LimitsInfo
	// NoCredential marks a profile that holds settings but no credential.
	NoCredential bool
	// Renewable marks a self-renewing credential (its expiry is not a fault).
	Renewable bool
	// Notice is the outcome of the last action on this profile (a switch
	// result, a refusal), shown first; NoticeErr styles it red.
	Notice    string
	NoticeErr bool
}

// LimitsInfo is the panel's LIMITS group: one row per window the provider
// reports, with the share left and the local reset time.
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
	// Left is the share left, 0-100, or -1 when the row has no share (credits).
	Left int
	// Column is the window's key in the accounts table, "" for a row that
	// is never a column (credits).
	Column string
}

// The panel never scrolls: a terminal too short for everything drops
// lines by priority, the least telling first, and the list keeps its rows.
const (
	detailPrioRare = 1 // created, penalty, browser: true but rarely useful
	detailPrioLow  = 3 // account label, notes
	detailPrioPath = 4
	detailPrioUse  = 5 // last used, credits
	detailPrioMid  = 6 // locked, errors, stale limits, a window the table already shows whole
	detailPrioHigh = 7 // the auth line, a window the table does not show whole
	detailPrioMust = 9 // the notice, a missing credential, the keys
)

// detailLine is one line of the panel and the priority that decides when
// a short terminal drops it. A group's heading is dropped with its last
// line, never on its own.
type detailLine struct {
	text  string
	prio  int
	title bool
}

// detailGroup is one heading and its lines: ACCOUNT, LIMITS or USAGE.
type detailGroup struct {
	title string
	lines []detailLine
}

// detailGutter separates the panel's columns.
const detailGutter = 2

// renderDetailPane draws the third panel: the selected account's details
// under the accounts list, in one box the height of `height` (border
// included). The account names the box; the limits' freshness sits at
// the right of the title like the list's did; the notice comes first,
// the keys last, and between them the groups run side by side when the
// terminal is wide enough and one under another when it is not.
func (m Model) renderDetailPane(g paneGeometry, height int, d *DetailInfo) string {
	ps := m.profilesPanel.styles
	inner := g.inner
	muted := m.styles.StatusText

	name := d.Name
	if d.Active {
		name = ps.ActiveIndicator.Render("● ") + ps.Title.MarginBottom(0).Render(name)
	} else {
		name = ps.Title.MarginBottom(0).Render(name)
	}
	right := ""
	if l := d.Limits; l != nil {
		switch {
		case !l.AsOf.IsZero():
			right = "limits as of " + l.AsOf.Format("15:04:05")
			if l.Loading {
				right += " (refreshing…)"
			}
		case l.Loading:
			right = "fetching limits…"
		}
		right = muted.Render(right)
	}
	gap := inner - lipgloss.Width(name) - lipgloss.Width(right)
	if gap < 1 {
		gap = 1
	}
	titleRow := ansi.Truncate(name+strings.Repeat(" ", gap)+right, inner, "…")

	lines := append([]string{titleRow}, m.detailBody(d, inner, height-3)...)
	for len(lines) < height-2 {
		lines = append(lines, "")
	}
	return ps.Border.Width(g.pane).Height(height - 2).Render(fitWidth(strings.Join(lines, "\n"), inner))
}

// detailBody is the panel's content under its title, fitted to avail
// lines and inner columns. It is what the height budget measures, so a
// very large avail returns everything the panel has to say.
func (m Model) detailBody(d *DetailInfo, inner, avail int) []string {
	var out []string
	if d.Notice != "" {
		style := m.styles.StatusSuccess
		if d.NoticeErr {
			style = m.styles.StatusError
		}
		out = append(out, ansi.Truncate(style.Render(d.Notice), inner, "…"))
	}
	legend := m.detailLegend(d)

	columns := m.detailColumns(d, inner)
	body := avail - len(out) - 1
	if body < 0 {
		body = 0
	}
	fitDetailColumns(columns, body)
	out = append(out, joinDetailColumns(columns, inner)...)
	out = append(out, ansi.Truncate(legend, inner, "…"))
	if len(out) > avail && avail >= 0 {
		out = out[:avail]
	}
	return out
}

// detailColumns lays the groups out for the width: three side by side
// (ACCOUNT, LIMITS, USAGE) when each gets forty columns, two when each
// gets forty with USAGE under ACCOUNT, otherwise one under another.
func (m Model) detailColumns(d *DetailInfo, inner int) [][]detailLine {
	account, limits, use := m.detailAccountGroup(d), m.detailLimitsGroup(d), m.detailUsageGroup(d)
	groups := []detailGroup{account, limits, use}
	if d.Limits == nil {
		groups = []detailGroup{account, use}
	}
	const minColumn = 40
	n := (inner + detailGutter) / (minColumn + detailGutter)
	if n > len(groups) {
		n = len(groups)
	}
	if n < 1 {
		n = 1
	}
	columns := make([][]detailLine, n)
	switch {
	case n == len(groups):
		for i, gr := range groups {
			columns[i] = gr.flatten()
		}
	case n == 2:
		// ACCOUNT and USAGE stack on the left; LIMITS has the right to itself.
		columns[0] = append(account.flatten(), use.flatten()...)
		columns[1] = limits.flatten()
	default:
		for _, gr := range groups {
			columns[0] = append(columns[0], gr.flatten()...)
		}
	}
	return columns
}

// flatten is the group's heading and lines, or nothing for an empty group.
func (gr detailGroup) flatten() []detailLine {
	if len(gr.lines) == 0 {
		return nil
	}
	return append([]detailLine{{text: gr.title, prio: detailPrioMust, title: true}}, gr.lines...)
}

// fitDetailColumns drops lines until every column fits avail: the
// lowest-priority line of the tallest column goes first, and a heading
// goes with its last line. Columns of nothing but must-keep lines are cut
// at the bottom as a last resort.
func fitDetailColumns(columns [][]detailLine, avail int) {
	for {
		tallest, height := -1, 0
		for i, c := range columns {
			if len(c) > height {
				tallest, height = i, len(c)
			}
		}
		if tallest < 0 || height <= avail {
			return
		}
		col := columns[tallest]
		drop, prio := -1, detailPrioMust+1
		for i, l := range col {
			if !l.title && l.prio <= prio {
				drop, prio = i, l.prio
			}
		}
		if drop < 0 {
			columns[tallest] = col[:avail]
			continue
		}
		col = append(col[:drop], col[drop+1:]...)
		columns[tallest] = pruneDetailTitles(col)
	}
}

// pruneDetailTitles removes a heading that no longer heads anything.
func pruneDetailTitles(col []detailLine) []detailLine {
	out := col[:0]
	for i, l := range col {
		if l.title && (i+1 >= len(col) || col[i+1].title) {
			continue
		}
		out = append(out, l)
	}
	return out
}

// joinDetailColumns renders the columns side by side, each cut to its
// width, with a gutter between them.
func joinDetailColumns(columns [][]detailLine, inner int) []string {
	n := len(columns)
	if n == 0 {
		return nil
	}
	width := (inner - detailGutter*(n-1)) / n
	if width < 1 {
		width = 1
	}
	height := 0
	for _, c := range columns {
		if len(c) > height {
			height = len(c)
		}
	}
	rows := make([]string, height)
	for r := 0; r < height; r++ {
		cells := make([]string, n)
		for i, c := range columns {
			text := ""
			if r < len(c) {
				text = c[r].text
			}
			cells[i] = padRight(ansi.Truncate(text, width, "…"), width)
		}
		rows[r] = strings.TrimRight(strings.Join(cells, strings.Repeat(" ", detailGutter)), " ")
	}
	return rows
}

// detailAccountGroup: who the account is and how it is signed in — the
// auth line the row cannot hold, a missing credential, a lock, notes,
// where the credential lives, and the browser it opens with.
func (m Model) detailAccountGroup(d *DetailInfo) detailGroup {
	muted := m.styles.StatusText
	sep := muted.Render(" · ")
	statusStyle := m.profilesPanel.styles.StatusStyle
	gr := detailGroup{title: m.profilesPanel.styles.Header.BorderBottom(false).Render("ACCOUNT")}
	add := func(text string, prio int) {
		gr.lines = append(gr.lines, detailLine{text: text, prio: prio})
	}

	if d.Account != "" && d.Account != d.Name {
		add(detailLabel(muted, "account")+d.Account, detailPrioLow)
	}

	auth := []string{muted.Render(d.AuthMode)}
	if d.PlanType != "" {
		auth = append(auth, muted.Render(d.PlanType))
	}
	auth = append(auth, statusStyle(d.HealthStatus).Render(formatStatusLabel(d.HealthStatus)))
	switch ttl := time.Until(d.TokenExpiry); {
	case d.TokenExpiry.IsZero():
	case ttl > 0:
		auth = append(auth, muted.Render("token "+strings.TrimSuffix(health.FormatTimeRemaining(d.TokenExpiry), " left")))
	case d.Renewable:
		auth = append(auth, muted.Render("token renews on next use"))
	default:
		auth = append(auth, m.styles.StatusError.Render("token expired"))
	}
	add(strings.Join(auth, sep), detailPrioHigh)

	if d.NoCredential {
		add(detailLabel(muted, "credential")+m.styles.StatusError.Render("none captured — press n and log in as this account"), detailPrioMust)
	}
	if d.Locked {
		add(muted.Render("locked"), detailPrioMid)
	}
	if d.Description != "" {
		add(detailLabel(muted, "notes")+d.Description, detailPrioLow)
	}
	if d.Path != "" {
		add(muted.Render(d.Path), detailPrioPath)
	}
	if d.BrowserCmd != "" || d.BrowserProf != "" {
		browser := d.BrowserCmd
		switch {
		case browser != "" && d.BrowserProf != "":
			browser += " (" + d.BrowserProf + ")"
		case browser == "":
			browser = d.BrowserProf
		}
		add(detailLabel(muted, "browser")+browser, detailPrioRare)
	}
	return gr
}

// detailLimitsGroup: every window the service reported, the share left
// coloured by how little that is, with the clock it resets at; or why
// there are none.
func (m Model) detailLimitsGroup(d *DetailInfo) detailGroup {
	gr := detailGroup{title: m.profilesPanel.styles.Header.BorderBottom(false).Render("LIMITS")}
	l := d.Limits
	if l == nil {
		return gr
	}
	muted := m.styles.StatusText
	add := func(text string, prio int) {
		gr.lines = append(gr.lines, detailLine{text: text, prio: prio})
	}
	// A window the table shows whole — figure and RESETS — is the first
	// to go from a short panel; one it shows only in part, or not at all,
	// is reachable nowhere else.
	shown := m.windowsShownWhole(d.Provider)
	for _, r := range l.Rows {
		prio := detailPrioHigh
		if r.Column != "" && shown[r.Column] {
			prio = detailPrioMid
		}
		figure, rest := r.Value, ""
		if i := strings.Index(r.Value, ", "); i >= 0 {
			figure, rest = r.Value[:i], r.Value[i:]
		}
		var style lipgloss.Style
		switch {
		case r.Severity == "critical":
			style = m.styles.StatusError
		case r.Severity == "warning":
			style = m.styles.StatusWarning
		case r.Left >= 0:
			style = m.percentStyle(r.Left)
		default:
			style = lipgloss.NewStyle()
		}
		add(muted.Render(padRight(r.Label, 14))+style.Render(figure)+muted.Render(rest), prio)
	}
	switch {
	case len(l.Rows) == 0 && l.Loading:
		add(muted.Render("fetching…"), detailPrioHigh)
	case len(l.Rows) == 0 && l.Err != "":
		add(m.styles.StatusWarning.Render(shortLimitsError(l.Err)), detailPrioHigh)
	case len(l.Rows) == 0:
		add(muted.Render("no windows reported"), detailPrioHigh)
	case l.Stale && l.Err != "":
		add(m.styles.StatusWarning.Render(fmt.Sprintf("last known as of %s: %s", l.AsOf.Format("15:04:05"), shortLimitsError(l.Err))), detailPrioMid)
	}
	return gr
}

// windowsShownWhole names the windows the accounts table shows with both
// their columns, figure and RESETS, at the current width.
func (m Model) windowsShownWhole(provider string) map[string]bool {
	cols := m.accountColumns(provider, m.profilesPanel.profiles, m.tier(), paneGeom(m.width).inner, time.Now())
	halves := make(map[string]int, len(cols))
	for _, c := range cols {
		if c.window != "" {
			halves[c.window]++
		}
	}
	whole := make(map[string]bool, len(halves))
	for window, n := range halves {
		whole[window] = n == 2
	}
	return whole
}

// detailUsageGroup: when the account was last used, its recent errors
// and penalty when there are any, and when it was created when known.
func (m Model) detailUsageGroup(d *DetailInfo) detailGroup {
	muted := m.styles.StatusText
	gr := detailGroup{title: m.profilesPanel.styles.Header.BorderBottom(false).Render("USAGE")}
	add := func(text string, prio int) {
		gr.lines = append(gr.lines, detailLine{text: text, prio: prio})
	}
	add(detailLabel(muted, "last used")+formatRelativeTime(d.LastUsedAt), detailPrioUse)
	if d.ErrorCount > 0 {
		style := m.styles.StatusWarning
		if d.ErrorCount >= 3 {
			style = m.styles.StatusError
		}
		add(detailLabel(muted, "errors")+style.Render(fmt.Sprintf("%d in last hour", d.ErrorCount)), detailPrioMid)
	}
	if d.Penalty > 0 {
		add(detailLabel(muted, "penalty")+fmt.Sprintf("%.2f", d.Penalty), detailPrioRare)
	}
	if !d.CreatedAt.IsZero() {
		add(detailLabel(muted, "created")+d.CreatedAt.Format("2006-01-02"), detailPrioRare)
	}
	return gr
}

// detailLabel is a group line's label: muted, padded so values align.
func detailLabel(muted lipgloss.Style, label string) string {
	return muted.Render(padRight(label, 11))
}

// detailLegend is the panel's last line: what the keys do to this
// account, from the tier's list. On an account the service refuses, r is
// the way to the login, and the legend says so.
func (m Model) detailLegend(d *DetailInfo) string {
	muted := m.styles.StatusText
	tier := m.tier()
	legend := make([]string, 0, len(tier.actions))
	for _, k := range tier.actions {
		label := actionLegend[k]
		if k == "r" && m.tokenInTrouble(d.Provider, d.Name) {
			label = "refresh, or re-login"
		}
		legend = append(legend, m.styles.StatusKey.Render(k)+muted.Render(" "+label))
	}
	return strings.Join(legend, "  ")
}

// shortLimitsError condenses a fetch error to a phrase that fits a panel
// row or a provider chip.
func shortLimitsError(err string) string {
	return usage.ShortError(err, 48)
}
