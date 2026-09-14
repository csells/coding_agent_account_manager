package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/health"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/usage"
)

// The main screen is split top to bottom: a strip of providers across the
// top, steered with ←/→, and the selected provider's accounts below,
// steered with ↑/↓ — one row per account with each rate-limit window as a
// column, with the selected account expanded in place — a tree of detail
// lines hanging off its row — so ↓ walks the accounts and reads each one.
// The strip's height is fixed by its content; the accounts pane takes the
// rest and scrolls by account.
//
// Three width tiers:
//   - wide (>= wideCols): provider cards three lines tall (name, active
//     account, tightest windows); every window column; LAST USED.
//   - medium (>= mediumCols): one-line provider chips that wrap; window
//     columns keep the percentage and a short reset; no LAST USED.
//   - narrow: a single row of provider tabs scrolled around the selected
//     one; the table shows STATUS and the TIGHTEST window only, and the
//     expansion lists every window.
const (
	wideCols          = 150
	mediumCols        = 100
	providerCardWidth = 30
)

type widthTier int

const (
	tierNarrow widthTier = iota
	tierMedium
	tierWide
)

// paneWidth is the width a full-width pane's style is given: the border
// adds two columns, and the last terminal column stays empty (see
// verticalPanels).
func paneWidth(termWidth int) int {
	return termWidth - 3
}

func (m Model) widthTier() widthTier {
	switch {
	case m.width >= wideCols:
		return tierWide
	case m.width >= mediumCols:
		return tierMedium
	}
	return tierNarrow
}

// verticalPanels renders the provider strip and the accounts pane, sized
// to exactly contentHeight lines.
func (m Model) verticalPanels(contentHeight int) string {
	// Pane border (2) and padding (2), and one column of slack: a line
	// that reaches the terminal's last column puts the terminal into its
	// pending-wrap state, after which the renderer's row accounting is
	// off by one and lines it skips as unchanged stay where they were —
	// rows of the strip interleaved with the row below them, as seen in
	// Terminal.app. Nothing rendered here may fill the last column.
	inner := m.width - 5
	if inner < 20 {
		inner = 20
	}
	strip := m.renderProviderStrip(inner)
	stripHeight := lipgloss.Height(strip)

	paneHeight := contentHeight - stripHeight - 1
	if paneHeight < 6 {
		paneHeight = 6
	}
	accounts := m.renderAccountsPane(inner, paneHeight)
	return lipgloss.JoinVertical(lipgloss.Left, strip, "", accounts)
}

// --- provider strip -------------------------------------------------------

// providerCard is what the strip says about one provider.
type providerCard struct {
	id       string
	label    string
	count    int
	selected bool
	active   string // active account, "" when none
	summary  string // tightest windows, or why there are none (unstyled)
	summaryS string // the same, styled
}

func (m Model) providerCards() []providerCard {
	now := time.Now()
	cards := make([]providerCard, 0, len(m.providers))
	for i, id := range m.providers {
		c := providerCard{id: id, label: providerLabel(id), count: len(m.profiles[id]), selected: i == m.activeProvider}
		for _, p := range m.profiles[id] {
			if p.IsActive {
				c.active = p.Name
			}
		}
		c.summary, c.summaryS = m.providerSummary(id, c.active, now)
		cards = append(cards, c)
	}
	return cards
}

// providerSummary is the one-line window summary for a provider's active
// account: "5h 53% · wk 44% · Fable 0%".
func (m Model) providerSummary(provider, active string, now time.Time) (plain, styled string) {
	muted := m.styles.StatusText
	if active == "" {
		if len(m.profiles[provider]) == 0 {
			return "not logged in", muted.Render("not logged in")
		}
		return "no active account", muted.Render("no active account")
	}
	if m.hooks.Limits == nil {
		return "", ""
	}
	e, ok := m.limits[limitsKey(provider, active)]
	if !ok || (e.loading && e.info == nil) {
		return "fetching…", muted.Render("fetching…")
	}
	cells := usage.WindowsOf(e.info)
	if len(cells) == 0 {
		msg := "no windows reported"
		if e.err != nil {
			msg = shortLimitsError(e.err.Error())
		}
		return msg, m.styles.StatusWarning.Render(msg)
	}
	var plainParts, styledParts []string
	for _, c := range cells {
		left := usage.PercentLeft(c.Window)
		p := fmt.Sprintf("%s %d%%", shortWindowLabel(c.Label), left)
		plainParts = append(plainParts, p)
		styledParts = append(styledParts, muted.Render(shortWindowLabel(c.Label))+" "+m.percentStyle(left).Render(fmt.Sprintf("%d%%", left)))
	}
	sep := muted.Render(" · ")
	return strings.Join(plainParts, " · "), strings.Join(styledParts, sep)
}

// shortWindowLabel abbreviates a window label for the strip: "5-hour" to
// "5h", "Weekly" to "wk", "Weekly Fable" to "Fable".
func shortWindowLabel(label string) string {
	switch label {
	case "5-hour":
		return "5h"
	case "Weekly":
		return "wk"
	case "Daily":
		return "day"
	case "Monthly":
		return "mo"
	}
	for _, prefix := range []string{"Weekly ", "Daily ", "Monthly ", "5-hour "} {
		if strings.HasPrefix(label, prefix) {
			return strings.TrimPrefix(label, prefix)
		}
	}
	return label
}

// percentStyle colours a percent-left figure: red near empty, amber low.
func (m Model) percentStyle(left int) lipgloss.Style {
	switch {
	case left <= 10:
		return m.styles.StatusError
	case left <= 25:
		return m.styles.StatusWarning
	}
	return lipgloss.NewStyle()
}

func (m Model) renderProviderStrip(inner int) string {
	ps := m.providerPanel.styles
	var body string
	switch m.widthTier() {
	case tierWide:
		body = lipgloss.JoinVertical(lipgloss.Left, ps.Title.Render(fmt.Sprintf("Providers (%d)", len(m.providers))), m.renderProviderCards(inner))
	case tierMedium:
		body = m.renderProviderChips(inner)
	default:
		body = m.renderProviderTabRow(inner)
	}
	return ps.Border.Width(paneWidth(m.width)).Render(body)
}

// renderProviderCards lays out three-line cards, wrapping into rows.
// Providers with no accounts share one muted card unless selected.
func (m Model) renderProviderCards(inner int) string {
	ps := m.providerPanel.styles
	cards := m.providerCards()

	perRow := (inner + 1) / (providerCardWidth + 1)
	if perRow < 1 {
		perRow = 1
	}
	cw := (inner - (perRow - 1)) / perRow
	if cw < providerCardWidth {
		cw = inner
		perRow = 1
	}

	// Each card is three plain lines cut to the card's text width, then
	// painted in one style; the unselected card's count and summary carry
	// their own colours on top.
	pad := ps.Item.GetHorizontalPadding()
	textW := cw - pad
	if textW < 8 {
		textW = 8
	}
	paint := func(style lipgloss.Style, text string) string {
		return padStyled(style.Render(truncateWithEllipsis(text, textW)), cw, style)
	}

	var folded []string
	var rendered []string
	for _, c := range cards {
		if c.count == 0 && !c.selected {
			folded = append(folded, c.label)
			continue
		}
		style := ps.Item
		if c.selected {
			style = ps.SelectedItem
		}
		// U+25B8, not U+25B6: the latter has an emoji presentation and some
		// terminals draw it two cells wide, which on a line padded to the
		// pane's width overflows it.
		marker := "  "
		if c.selected {
			marker = "▸ "
		}
		l1 := paint(style, fmt.Sprintf("%s%s (%d)", marker, c.label, c.count))
		var l2, l3 string
		switch {
		case c.count == 0:
			l2 = paint(style, "no accounts yet")
			l3 = paint(style, fmt.Sprintf("caam backup %s <email>", c.id))
		case c.active == "":
			l2 = paint(style, "no active account")
			l3 = paint(style, c.summary)
		default:
			l2 = paint(style, "● "+c.active)
			if c.selected || lipgloss.Width(c.summary) > textW {
				l3 = paint(style, c.summary)
			} else {
				l3 = padStyled(style.Render("")+c.summaryS, cw, style)
			}
		}
		rendered = append(rendered, lipgloss.JoinVertical(lipgloss.Left, l1, l2, l3))
	}
	if len(folded) > 0 {
		muted := m.styles.StatusText
		text := strings.Join(folded, " · ")
		l1 := paint(muted, "  not logged in")
		l2 := paint(muted, text)
		l3 := paint(muted, "")
		if lipgloss.Width(text) > textW {
			// Split the names over the two remaining lines.
			if cut := strings.LastIndex(text[:textW], " · "); cut > 0 {
				l2 = paint(muted, text[:cut])
				l3 = paint(muted, strings.TrimPrefix(text[cut:], " · "))
			}
		}
		rendered = append(rendered, lipgloss.JoinVertical(lipgloss.Left, l1, l2, l3))
	}

	var rows []string
	for i := 0; i < len(rendered); i += perRow {
		end := i + perRow
		if end > len(rendered) {
			end = len(rendered)
		}
		parts := make([]string, 0, 2*(end-i))
		for j := i; j < end; j++ {
			if j > i {
				parts = append(parts, " ")
			}
			parts = append(parts, rendered[j])
		}
		rows = append(rows, lipgloss.JoinHorizontal(lipgloss.Top, parts...))
	}
	return lipgloss.JoinVertical(lipgloss.Left, rows...)
}

// renderProviderChips lays out one-line chips, wrapping by width.
func (m Model) renderProviderChips(inner int) string {
	ps := m.providerPanel.styles
	var chips []string
	var widths []int
	for _, c := range m.providerCards() {
		style := ps.Item
		text := fmt.Sprintf("%s %d", c.label, c.count)
		if c.count == 0 && !c.selected {
			chips = append(chips, m.styles.StatusText.Render(" "+text+" "))
			widths = append(widths, lipgloss.Width(text)+2)
			continue
		}
		if c.selected {
			style = ps.SelectedItem
			text = "▸ " + text
		}
		var chip string
		if c.summary != "" && c.count > 0 {
			summary := c.summaryS
			if c.selected || lipgloss.Width(c.summary) > 28 {
				summary = style.Render(truncateWithEllipsis(c.summary, 28))
			}
			chip = style.Render(" "+text+" ") + m.styles.StatusText.Inherit(style).Render("· ") + summary + style.Render(" ")
		} else {
			chip = style.Render(" " + text + " ")
		}
		chips = append(chips, chip)
		widths = append(widths, lipgloss.Width(chip))
	}
	var rows []string
	var row []string
	used := 0
	for i, chip := range chips {
		w := widths[i]
		if used > 0 && used+1+w > inner {
			rows = append(rows, lipgloss.JoinHorizontal(lipgloss.Top, row...))
			row, used = nil, 0
		}
		if used > 0 {
			row = append(row, " ")
			used++
		}
		row = append(row, chip)
		used += w
	}
	if len(row) > 0 {
		rows = append(rows, lipgloss.JoinHorizontal(lipgloss.Top, row...))
	}
	return lipgloss.JoinVertical(lipgloss.Left, rows...)
}

// renderProviderTabRow is the narrow strip: one row of tabs, scrolled so
// the selected one is visible, with ‹ › marking tabs off either edge.
func (m Model) renderProviderTabRow(inner int) string {
	ps := m.providerPanel.styles
	cards := m.providerCards()
	labels := make([]string, len(cards))
	for i, c := range cards {
		labels[i] = fmt.Sprintf("%s %d", c.label, c.count)
	}
	width := func(i int) int { return lipgloss.Width(labels[i]) + 2 }

	// Grow a window around the selected tab until it no longer fits.
	start, end := m.activeProvider, m.activeProvider+1
	if start < 0 || start >= len(cards) {
		start, end = 0, min(1, len(cards))
	}
	total := width(start) + 4
	for {
		grew := false
		if end < len(cards) && total+1+width(end) <= inner {
			total += 1 + width(end)
			end++
			grew = true
		}
		if start > 0 && total+1+width(start-1) <= inner {
			total += 1 + width(start-1)
			start--
			grew = true
		}
		if !grew {
			break
		}
	}

	var parts []string
	if start > 0 {
		parts = append(parts, m.styles.StatusText.Render("‹ "))
	} else {
		parts = append(parts, "  ")
	}
	for i := start; i < end; i++ {
		if i > start {
			parts = append(parts, " ")
		}
		c := cards[i]
		switch {
		case c.selected:
			parts = append(parts, ps.SelectedItem.Render(" ▸ "+labels[i]+" "))
		case c.count == 0:
			parts = append(parts, m.styles.StatusText.Render(" "+labels[i]+" "))
		default:
			parts = append(parts, ps.Item.Render(" "+labels[i]+" "))
		}
	}
	if end < len(cards) {
		parts = append(parts, m.styles.StatusText.Render(" ›"))
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, parts...)
}

// --- accounts pane --------------------------------------------------------

// accountColumn is one column of the accounts table.
type accountColumn struct {
	header string
	width  int
	cells  []string // one per profile, unstyled
	styles []lipgloss.Style
	// window is set for a rate-limit column (its usage column name).
	window string
	prio   int // drop order when the table is too wide: higher goes first
}

func (m Model) renderAccountsPane(inner, height int) string {
	ps := m.profilesPanel.styles
	title := lipgloss.NewStyle().Bold(true).Foreground(m.theme.Palette.Accent)
	muted := m.styles.StatusText
	provider := m.currentProvider()
	profiles := m.profilesPanel.profiles
	tier := m.widthTier()
	now := time.Now()

	// Title row: "<Provider> accounts" left, freshness right.
	left := title.Render(providerLabel(provider) + " accounts")
	right := ""
	if m.hooks.Limits != nil && len(profiles) > 0 {
		if info := m.selectedProfileInfo(); info != nil {
			if e, ok := m.limits[limitsKey(provider, info.Name)]; ok && !e.at.IsZero() {
				right = muted.Render("limits as of " + e.at.Format("15:04:05"))
				if e.loading {
					right += muted.Render(" (refreshing…)")
				}
			} else if ok && e.loading {
				right = muted.Render("fetching limits…")
			}
		}
	}
	gap := inner - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		gap = 1
	}
	titleRow := left + strings.Repeat(" ", gap) + right

	lines := []string{titleRow}
	bodyHeight := height - 2 - 1 // border, title
	if len(profiles) == 0 {
		lines = append(lines, ps.Empty.Render(emptyProfilesMessage(provider)))
		return ps.Border.Width(paneWidth(m.width)).Height(height - 2).Render(lipgloss.JoinVertical(lipgloss.Left, lines...))
	}

	tableRows := bodyHeight - 1 // header
	if tableRows < 1 {
		tableRows = 1
	}

	cols := m.accountColumns(provider, profiles, tier, inner, now)

	// Header.
	headerCells := make([]string, len(cols))
	for i, c := range cols {
		headerCells[i] = padRight(truncateWithEllipsis(c.header, c.width), c.width)
	}
	lines = append(lines, ps.Header.Render(padRight(strings.Join(headerCells, " "), inner)))

	// One block per account: its row, and — for the selected one — the
	// tree of detail lines expanded beneath it. The list scrolls by
	// block so the selected account and its expansion stay in view.
	sel := m.profilesPanel.GetSelected()
	blocks := make([][]string, len(profiles))
	for i := range profiles {
		var rowStyle lipgloss.Style
		switch {
		case i == sel:
			rowStyle = ps.SelectedRow
		case i%2 == 1:
			rowStyle = ps.RowAlt
		default:
			rowStyle = ps.Row
		}
		cells := make([]string, len(cols))
		for j, c := range cols {
			cell := ""
			if i < len(c.cells) {
				cell = c.cells[i]
			}
			cellStyle := ps.Row
			if i < len(c.styles) {
				cellStyle = c.styles[i]
			}
			cells[j] = cellStyle.Inherit(rowStyle).Render(padRight(truncateWithEllipsis(cell, c.width), c.width))
		}
		row := strings.Join(cells, rowStyle.Render(" "))
		blocks[i] = []string{padStyled(row, inner, rowStyle)}
		if i == sel {
			blocks[i] = append(blocks[i], m.expandedLines(provider, &profiles[i], inner, tier, now)...)
		}
	}

	// Scroll: start at the first block that lets the selected block end
	// within tableRows, preferring to show blocks above it.
	startBlock := 0
	if sel >= 0 && sel < len(blocks) {
		used := len(blocks[sel])
		for startBlock = sel; startBlock > 0; startBlock-- {
			if used+len(blocks[startBlock-1]) > tableRows {
				break
			}
			used += len(blocks[startBlock-1])
		}
	}
	shown := 0
	for i := startBlock; i < len(blocks) && shown < tableRows; i++ {
		for _, line := range blocks[i] {
			if shown >= tableRows {
				break
			}
			lines = append(lines, line)
			shown++
		}
	}
	for ; shown < tableRows; shown++ {
		lines = append(lines, "")
	}

	return ps.Border.Width(paneWidth(m.width)).Height(height - 2).Render(lipgloss.JoinVertical(lipgloss.Left, lines...))
}

// accountColumns builds the table's columns for the tier and fits them to
// inner by dropping the least important ones from the right.
func (m Model) accountColumns(provider string, profiles []ProfileInfo, tier widthTier, inner int, now time.Time) []accountColumn {
	ps := m.profilesPanel.styles
	n := len(profiles)

	name := accountColumn{header: "NAME", cells: make([]string, n), styles: make([]lipgloss.Style, n), prio: 0}
	status := accountColumn{header: "STATUS", cells: make([]string, n), styles: make([]lipgloss.Style, n), prio: 0}
	for i, p := range profiles {
		mark := "  "
		if p.IsActive {
			mark = "● "
		}
		name.cells[i] = mark + p.Name
		name.styles[i] = ps.RowLabel
		if p.IsActive {
			name.styles[i] = ps.ActiveIndicator
		}
		status.cells[i] = formatTUIStatus(&profiles[i])
		status.styles[i] = ps.StatusStyle(p.HealthStatus)
	}
	cols := []accountColumn{name, status}

	if m.hooks.Limits != nil {
		if tier == tierNarrow {
			tight := accountColumn{header: "TIGHTEST", cells: make([]string, n), styles: make([]lipgloss.Style, n), prio: 1}
			for i, p := range profiles {
				tight.cells[i], tight.styles[i] = m.tightestCell(provider, p.Name, now)
			}
			cols = append(cols, tight)
		} else {
			for k, col := range m.windowColumnsFor(provider, profiles) {
				wc := accountColumn{header: col, window: col, cells: make([]string, n), styles: make([]lipgloss.Style, n), prio: 2 + k}
				for i, p := range profiles {
					wc.cells[i], wc.styles[i] = m.windowCell(provider, p.Name, col, tier, now)
				}
				cols = append(cols, wc)
			}
		}
	}
	if tier == tierWide {
		lu := accountColumn{header: "LAST USED", cells: make([]string, n), styles: make([]lipgloss.Style, n), prio: 1}
		for i, p := range profiles {
			lu.cells[i] = formatRelativeTime(p.LastUsed)
			lu.styles[i] = ps.RowMetadata
		}
		cols = append(cols, lu)
	}

	// Natural widths.
	for i := range cols {
		w := lipgloss.Width(cols[i].header)
		for _, c := range cols[i].cells {
			if cw := lipgloss.Width(c); cw > w {
				w = cw
			}
		}
		cols[i].width = w
	}
	if cols[0].width > 34 {
		cols[0].width = 34
	}

	total := func() int {
		t := len(cols) - 1
		for _, c := range cols {
			t += c.width
		}
		return t
	}
	// Drop by priority (highest first, rightmost among equals) until it fits.
	for total() > inner && len(cols) > 2 {
		drop, best := -1, 0
		for i := len(cols) - 1; i >= 2; i-- {
			if cols[i].prio > best {
				best, drop = cols[i].prio, i
			}
		}
		if drop < 0 {
			break
		}
		cols = append(cols[:drop], cols[drop+1:]...)
	}
	// Then narrow the name column, never below 12.
	for total() > inner && cols[0].width > 12 {
		cols[0].width--
	}
	return cols
}

// windowColumnsFor is the union of window columns across a provider's
// accounts, in rank order.
func (m Model) windowColumnsFor(provider string, profiles []ProfileInfo) []string {
	ranks := make(map[string]int)
	for _, p := range profiles {
		e, ok := m.limits[limitsKey(provider, p.Name)]
		if !ok {
			continue
		}
		for _, c := range usage.WindowsOf(e.info) {
			if _, seen := ranks[c.Column]; !seen {
				ranks[c.Column] = c.Rank
			}
		}
	}
	cols := make([]string, 0, len(ranks))
	for c := range ranks {
		cols = append(cols, c)
	}
	sort.Slice(cols, func(i, j int) bool {
		if ranks[cols[i]] != ranks[cols[j]] {
			return ranks[cols[i]] < ranks[cols[j]]
		}
		return cols[i] < cols[j]
	})
	return cols
}

// windowCell renders one account's figure for a window column.
func (m Model) windowCell(provider, profile, column string, tier widthTier, now time.Time) (string, lipgloss.Style) {
	e, ok := m.limits[limitsKey(provider, profile)]
	if !ok || (e.loading && e.info == nil) {
		return "…", m.styles.StatusText
	}
	for _, c := range usage.WindowsOf(e.info) {
		if c.Column != column {
			continue
		}
		left := usage.PercentLeft(c.Window)
		text := fmt.Sprintf("%d%%", left)
		if tier == tierWide {
			text += " left"
		}
		if !c.Window.ResetsAt.IsZero() {
			text += " · " + usage.LocalReset(c.Window.ResetsAt, now)
		}
		if e.stale {
			text += " *"
		}
		return text, m.percentStyle(left)
	}
	return "-", m.styles.StatusText
}

// tightestCell is the narrow tier's one figure: the window closest to its cap.
func (m Model) tightestCell(provider, profile string, now time.Time) (string, lipgloss.Style) {
	e, ok := m.limits[limitsKey(provider, profile)]
	if !ok || (e.loading && e.info == nil) {
		return "…", m.styles.StatusText
	}
	if e.info == nil || e.info.MostConstrainedWindow() == nil {
		if e.err != nil {
			return shortLimitsError(e.err.Error()), m.styles.StatusWarning
		}
		return "-", m.styles.StatusText
	}
	w := e.info.MostConstrainedWindow()
	label := ""
	for _, c := range usage.WindowsOf(e.info) {
		if c.Window == w {
			label = shortWindowLabel(c.Label)
		}
	}
	left := usage.PercentLeft(w)
	text := fmt.Sprintf("%s %d%%", label, left)
	if !w.ResetsAt.IsZero() {
		text += " · " + usage.LocalReset(w.ResetsAt, now)
	}
	return text, m.percentStyle(left)
}

// expandedLines is the tree of detail lines under the selected account:
// what its row does not say — the outcome of the last action on it, its
// windows (narrow tier, where the row shows one), auth and token, where
// it lives, and what the keys do. Each line hangs off the row with a
// tree glyph; the last uses └.
func (m Model) expandedLines(provider string, info *ProfileInfo, inner int, tier widthTier, now time.Time) []string {
	muted := m.styles.StatusText
	key := func(k, what string) string { return m.styles.StatusKey.Render(k) + muted.Render(" "+what) }
	sep := muted.Render(" · ")
	var items []string

	if m.notice != "" && m.noticeKey == limitsKey(provider, info.Name) {
		style := m.styles.StatusSuccess
		if m.noticeErr {
			style = m.styles.StatusError
		}
		items = append(items, style.Render(m.notice))
	}
	if info.NoCredential {
		items = append(items, m.styles.StatusError.Render(fmt.Sprintf("no credential captured — log in as this account, then: caam backup %s %s", provider, info.Name)))
	}

	// Windows: the narrow tier's row shows only the tightest one, so list
	// them all here; wider tiers already have them as columns, and only
	// note when the figures are stale or missing.
	if e, ok := m.limits[limitsKey(provider, info.Name)]; ok && m.hooks.Limits != nil {
		cells := usage.WindowsOf(e.info)
		switch {
		case tier == tierNarrow && len(cells) > 0:
			for _, c := range cells {
				left := usage.PercentLeft(c.Window)
				line := muted.Render(padRight(c.Label, 14)) + m.percentStyle(left).Render(fmt.Sprintf("%3d%% left", left))
				if !c.Window.ResetsAt.IsZero() {
					line += muted.Render(", resets " + usage.LocalReset(c.Window.ResetsAt, now))
				}
				items = append(items, line)
			}
		case len(cells) == 0 && e.err != nil:
			items = append(items, m.styles.StatusWarning.Render("limits: "+shortLimitsError(e.err.Error())))
		case len(cells) == 0 && e.loading:
			items = append(items, muted.Render("limits: fetching…"))
		}
		if e.stale && e.err != nil {
			items = append(items, m.styles.StatusWarning.Render(fmt.Sprintf("limits are last known as of %s: %s", e.at.Format("15:04:05"), shortLimitsError(e.err.Error()))))
		}
	}

	// Auth, plan, health, token.
	auth := []string{muted.Render(info.AuthMode)}
	if h := m.healthFor(provider, info.Name); h != nil {
		if h.PlanType != "" {
			auth = append(auth, muted.Render(h.PlanType))
		}
		auth = append(auth, ps(m).StatusStyle(info.HealthStatus).Render(formatStatusLabel(info.HealthStatus)))
		switch ttl := time.Until(h.TokenExpiresAt); {
		case h.TokenExpiresAt.IsZero():
		case ttl > 0:
			auth = append(auth, muted.Render("token "+strings.TrimSuffix(health.FormatTimeRemaining(h.TokenExpiresAt), " left")))
		case h.CredentialRenewable():
			auth = append(auth, muted.Render("token renews on next use"))
		default:
			auth = append(auth, m.styles.StatusError.Render("token expired"))
		}
	} else {
		auth = append(auth, ps(m).StatusStyle(info.HealthStatus).Render(formatStatusLabel(info.HealthStatus)))
	}
	items = append(items, strings.Join(auth, sep))

	// Where it lives, and when it was last used (the wide tier's column
	// already says; the others get it here).
	if path := m.vaultPathFor(provider, info.Name); path != "" {
		items = append(items, muted.Render(path))
	}
	if tier != tierWide {
		items = append(items, muted.Render("last used "+formatRelativeTime(info.LastUsed)))
	}

	// Actions.
	switch tier {
	case tierNarrow:
		items = append(items, strings.Join([]string{key("enter", "switch"), key("b", "re-capture"), key("l", "login"), key("i", "card")}, "  "))
	case tierMedium:
		items = append(items, strings.Join([]string{key("enter", "switch"), key("b", "re-capture"), key("l", "login"), key("e", "edit"), key("d", "delete"), key("i", "card")}, "  "))
	default:
		items = append(items, strings.Join([]string{key("enter", "switch to this account"), key("b", "re-capture"), key("l", "login/refresh"), key("e", "edit"), key("o", "browser"), key("d", "delete"), key("i", "full card")}, "   "))
	}

	lines := make([]string, len(items))
	for i, item := range items {
		glyph := "├─ "
		if i == len(items)-1 {
			glyph = "└─ "
		}
		lines[i] = truncateStyledWidth("  "+muted.Render(glyph)+item, inner)
	}
	return lines
}

func ps(m Model) ProfilesPanelStyles { return m.profilesPanel.styles }

// vaultPathFor is the profile's vault directory, shortened with ~.
func (m Model) vaultPathFor(provider, name string) string {
	if m.vaultPath == "" {
		return ""
	}
	path := m.vaultPath + "/" + provider + "/" + name
	if i := strings.Index(path, "/.local/share/caam/vault/"); i >= 0 {
		return "~/vault/" + path[i+len("/.local/share/caam/vault/"):]
	}
	return path
}

// --- small rendering helpers ---------------------------------------------

// padStyled pads an already-styled line to width using the row style, so a
// background reaches the pane's edge.
func padStyled(s string, width int, style lipgloss.Style) string {
	if pad := width - lipgloss.Width(s); pad > 0 {
		return s + style.Render(strings.Repeat(" ", pad))
	}
	return s
}

func stripANSI(s string) string {
	var b strings.Builder
	inEsc := false
	for _, r := range s {
		switch {
		case inEsc:
			if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') {
				inEsc = false
			}
		case r == 0x1b:
			inEsc = true
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// truncateStyledWidth cuts a styled line to width by visible text.
func truncateStyledWidth(s string, width int) string {
	if lipgloss.Width(s) <= width {
		return s
	}
	return truncateWithEllipsis(stripANSI(s), width)
}

// --- limits prefetch --------------------------------------------------------

// limitsFetchFor starts a fetch for one profile when the cached entry is
// missing, stale or errored; nil otherwise.
func (m *Model) limitsFetchFor(provider, profile string) tea.Cmd {
	if m.hooks.Limits == nil || provider == "" || profile == "" {
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
		ctx, cancel := contextWithTimeout(30 * time.Second)
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
		delete(m.limits, limitsKey(provider, p.Name))
	}
	for _, id := range m.providers {
		for _, p := range m.profiles[id] {
			if p.IsActive {
				delete(m.limits, limitsKey(id, p.Name))
			}
		}
	}
}
