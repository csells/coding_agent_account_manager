package tui

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/health"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/usage"
)

// The main screen is split top to bottom: a strip of providers across the
// top, steered with ←/→, and the selected provider's accounts below,
// steered with ↑/↓ — one row per account with each rate-limit window as a
// column, with the selected account expanded in place as a tree of detail
// lines. The strip's height is fixed by its kind; the accounts pane takes
// the rest and scrolls by account.
//
// The strip is a tab strip: every provider has a slot, in key order, whose
// position never depends on what is selected. Slots sit in ONE row that
// scrolls horizontally; the scroll offset is kept on the model and moves
// only when the selection leaves the visible window, and the edges show
// how many providers are off-screen ("‹ 2", "3 ›").

// layoutTier is what the terminal's width allows the accounts table to
// show. One table, so a change to a tier is one edit.
type layoutTier struct {
	// longCells spells a window cell "53% left · 6:10 PM" rather than
	// "53% · 6:10 PM".
	longCells bool
	// allWindowColumns gives every reported window its own column; else
	// the table shows only the TIGHTEST one and the expansion lists them.
	allWindowColumns bool
	showLastUsed     bool
	// actions are the keys the expansion's legend explains.
	actions []string
}

const (
	wideCols   = 150
	mediumCols = 100
)

var (
	tierWide   = layoutTier{longCells: true, allWindowColumns: true, showLastUsed: true, actions: []string{"enter", "b", "l", "e", "o", "d", "i", "r"}}
	tierMedium = layoutTier{allWindowColumns: true, actions: []string{"enter", "b", "l", "e", "d", "i", "r"}}
	tierNarrow = layoutTier{actions: []string{"enter", "b", "l", "i"}}
)

// actionLegend spells the expansion's key legend, in the tier's order.
var actionLegend = map[string]string{
	"enter": "switch to this account", "b": "re-capture", "l": "login/refresh",
	"e": "edit", "o": "browser", "d": "delete", "i": "full card", "r": "refresh limits",
}

func (m Model) tier() layoutTier {
	switch {
	case m.width >= wideCols:
		return tierWide
	case m.width >= mediumCols:
		return tierMedium
	}
	return tierNarrow
}

// stripKind is how each provider's slot is drawn.
type stripKind int

const (
	// stripTabs: one line, "Label n".
	stripTabs stripKind = iota
	// stripChips: one line, "Label n · 5h 53% · wk 44%".
	stripChips
	// stripCards: three lines — name, active account, windows.
	stripCards
)

// stripKind picks the slot shape from the terminal's width and height:
// cards want a wide terminal and room below them for the table.
func (m Model) stripKind() stripKind {
	switch {
	case m.width >= wideCols && m.height >= 22:
		return stripCards
	case m.width >= mediumCols && m.height >= 14:
		return stripChips
	}
	return stripTabs
}

func (k stripKind) lines() int {
	if k == stripCards {
		return 3
	}
	return 1
}

// Slot widths. A card's width is fixed so a wider terminal shows more
// cards rather than wider ones.
const (
	providerCardWidth = 30
	chipSummaryWidth  = 28
	// Column bounds for the accounts table's NAME column.
	maxNameWidth = 34
	minNameWidth = 12
	// minPaneHeight is border (2) + title (1) + a one-line header + one
	// account row: what a 12-row terminal leaves the pane.
	minPaneHeight = 5
)

// paneGeometry is the one place the pane widths come from: the terminal
// width, the width handed to a bordered pane's style (the border adds two
// columns), and the content width inside the border and its padding.
type paneGeometry struct{ term, pane, inner int }

func paneGeom(termWidth int) paneGeometry {
	g := paneGeometry{term: termWidth, pane: termWidth - 2, inner: termWidth - 4}
	if g.inner < 20 {
		g.inner = 20
	}
	return g
}

// verticalPanels renders the provider strip and the accounts pane, sized
// to exactly contentHeight lines.
func (m Model) verticalPanels(contentHeight int) string {
	g := paneGeom(m.width)
	strip := m.renderProviderStrip(g)
	stripHeight := lipgloss.Height(strip)

	gap := 1
	paneHeight := contentHeight - stripHeight - gap
	if paneHeight < minPaneHeight {
		gap = 0
		paneHeight = contentHeight - stripHeight
	}
	if paneHeight < 3 {
		// Too short for a pane at all; draw the smallest frame and let
		// the caller clamp. Nothing can make a 6-row terminal useful.
		paneHeight = 3
	}
	accounts := m.renderAccountsPane(g, paneHeight)
	if gap == 0 {
		return lipgloss.JoinVertical(lipgloss.Left, strip, accounts)
	}
	return lipgloss.JoinVertical(lipgloss.Left, strip, "", accounts)
}

// --- provider strip -------------------------------------------------------

// stripItem is one provider's slot.
type stripItem struct {
	id       string
	label    string
	count    int
	selected bool
	active   string // active account, "" when none
	summary  string // tightest windows, or why there are none (plain)
	summaryS string // the same, coloured
}

func (m Model) stripItems() []stripItem {
	now := time.Now()
	items := make([]stripItem, 0, len(m.providers))
	for i, id := range m.providers {
		it := stripItem{id: id, label: providerLabel(id), count: len(m.profiles[id]), selected: i == m.activeProvider}
		for _, p := range m.profiles[id] {
			if p.IsActive {
				it.active = p.Name
			}
		}
		it.summary, it.summaryS = m.providerSummary(id, it.active, now)
		items = append(items, it)
	}
	return items
}

// providerSummary is the one-line window summary for a provider's active
// account: "5h 53% · wk 44% · Fable 0%".
func (m Model) providerSummary(provider, active string, now time.Time) (plain, styled string) {
	muted := m.styles.StatusText
	if active == "" {
		if len(m.profiles[provider]) == 0 {
			return "no accounts yet", muted.Render("no accounts yet")
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
		short := shortWindowLabel(c.Label)
		plainParts = append(plainParts, fmt.Sprintf("%s %d%%", short, left))
		styledParts = append(styledParts, muted.Render(short)+" "+m.percentStyle(left).Render(fmt.Sprintf("%d%%", left)))
	}
	return strings.Join(plainParts, " · "), strings.Join(styledParts, muted.Render(" · "))
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

// slotWidth is a provider's slot width for a strip kind. It depends on
// the item's text, never on whether it is selected, so slots keep their
// place as the selection moves.
func (m Model) slotWidth(kind stripKind, it stripItem, inner int) int {
	frame := m.stripStyles.Item.GetHorizontalFrameSize()
	switch kind {
	case stripCards:
		return min(providerCardWidth, inner)
	case stripChips:
		w := frame + 2 + lipgloss.Width(it.label) + 1 + len(strconv.Itoa(it.count))
		if it.summary != "" && it.count > 0 {
			w += 3 + min(lipgloss.Width(it.summary), chipSummaryWidth)
		}
		return min(w+1, inner)
	default:
		return min(frame+2+lipgloss.Width(it.label)+1+len(strconv.Itoa(it.count))+1, inner)
	}
}

// stripWindow decides which slots are visible: the scroll offset kept on
// the model, moved only as far as needed to keep the selection in view,
// and how many slots fit from there once the edge indicators have their
// room.
func (m Model) stripWindow(kind stripKind, items []stripItem, inner int) (offset, count int) {
	n := len(items)
	if n == 0 {
		return 0, 0
	}
	widths := make([]int, n)
	for i, it := range items {
		widths[i] = m.slotWidth(kind, it, inner)
	}
	indicator := func(hidden int) int {
		if hidden <= 0 {
			return 0
		}
		return lipgloss.Width("‹ ") + len(strconv.Itoa(hidden)) + 1 // "‹ 2 " / " 2 ›"
	}
	// fit reports how many slots from offset fit, with room for the
	// indicators the result implies.
	fit := func(offset int) int {
		avail := inner - indicator(offset)
		count, used := 0, 0
		for i := offset; i < n; i++ {
			need := widths[i]
			if count > 0 {
				need++
			}
			if used+need > avail {
				break
			}
			used += need
			count++
		}
		// The right indicator takes its room from the last slot(s).
		for count > 1 && offset+count < n && used+indicator(n-offset-count) > avail {
			used -= widths[offset+count-1] + 1
			count--
		}
		if count == 0 {
			count = 1
		}
		return count
	}

	sel := m.activeProvider
	if sel < 0 || sel >= n {
		sel = 0
	}
	offset = m.stripOffset
	if offset < 0 || offset >= n {
		offset = 0
	}
	if sel < offset {
		offset = sel
	}
	for count = fit(offset); sel >= offset+count && offset < sel; count = fit(offset) {
		offset++
	}
	// Do not leave slots hidden on the left while the row has room.
	for offset > 0 && fit(offset-1) >= sel-offset+2 {
		offset--
		count = fit(offset)
	}
	return offset, count
}

// settleStrip records the scroll offset the strip will draw with, so it
// persists across frames and moves only when the selection leaves it.
func (m *Model) settleStrip() {
	if m.width <= 0 {
		return
	}
	m.stripOffset, _ = m.stripWindow(m.stripKind(), m.stripItems(), paneGeom(m.width).inner)
}

func (m Model) renderProviderStrip(g paneGeometry) string {
	ss := m.stripStyles
	kind := m.stripKind()
	items := m.stripItems()
	offset, count := m.stripWindow(kind, items, g.inner)
	n := len(items)

	// Slots, each a block of kind.lines() lines at its slot width.
	blocks := make([][]string, 0, count)
	for i := offset; i < offset+count && i < n; i++ {
		blocks = append(blocks, m.renderSlot(kind, items[i], m.slotWidth(kind, items[i], g.inner)))
	}

	muted := m.styles.StatusText
	left, right := "", ""
	if offset > 0 {
		left = muted.Render("‹ " + strconv.Itoa(offset) + " ")
	}
	if rest := n - offset - count; rest > 0 {
		right = muted.Render(" " + strconv.Itoa(rest) + " ›")
	}
	// Indicators sit on the middle line of a card, the only line of a tab.
	indicatorLine := kind.lines() / 2

	lines := make([]string, kind.lines())
	for row := range lines {
		parts := make([]string, 0, 2*len(blocks)+2)
		if left != "" {
			if row == indicatorLine {
				parts = append(parts, left)
			} else {
				parts = append(parts, strings.Repeat(" ", lipgloss.Width(left)))
			}
		}
		for j, b := range blocks {
			if j > 0 {
				parts = append(parts, " ")
			}
			parts = append(parts, b[row])
		}
		line := strings.Join(parts, "")
		if right != "" && row == indicatorLine {
			pad := g.inner - lipgloss.Width(line) - lipgloss.Width(right)
			if pad < 0 {
				pad = 0
			}
			line += strings.Repeat(" ", pad) + right
		}
		lines[row] = ansi.Truncate(line, g.inner, "")
	}
	body := strings.Join(lines, "\n")
	if kind == stripCards {
		body = lipgloss.JoinVertical(lipgloss.Left, ss.Title.MarginBottom(0).Render(fmt.Sprintf("Providers (%d)", n)), body)
	}
	return ss.Border.Width(g.pane).Render(body)
}

// renderSlot draws one provider's slot: plain text cut to the slot's text
// width, painted once in the slot's style, and padded to the slot width
// in that style's background. Nothing styled is ever measured or cut.
func (m Model) renderSlot(kind stripKind, it stripItem, width int) []string {
	ss := m.stripStyles
	// Every slot style carries Item's frame, so a slot's text starts at the
	// same column whether it is selected, idle, or empty.
	pad := ss.Item.GetPaddingLeft()
	style := ss.Item
	switch {
	case it.selected:
		style = ss.SelectedItem.PaddingLeft(pad)
	case it.count == 0:
		style = m.styles.StatusText.PaddingLeft(pad)
	}
	textW := width - style.GetHorizontalFrameSize()
	if textW < 4 {
		textW = 4
	}
	paint := func(text string) string {
		return padStyled(style.Render(truncateWithEllipsis(text, textW)), width, style.GetBackground())
	}
	marker := "  "
	if it.selected {
		// U+25B8, not U+25B6: the latter has an emoji presentation and some
		// terminals draw it two cells wide.
		marker = "▸ "
	}
	name := fmt.Sprintf("%s%s %d", marker, it.label, it.count)

	switch kind {
	case stripCards:
		l1 := paint(fmt.Sprintf("%s%s (%d)", marker, it.label, it.count))
		var l2, l3 string
		switch {
		case it.count == 0:
			l2 = paint("no accounts yet")
			l3 = paint(fmt.Sprintf("caam backup %s <email>", it.id))
		case it.active == "":
			l2 = paint("no active account")
			l3 = paint(it.summary)
		default:
			l2 = paint("● " + it.active)
			if !it.selected && lipgloss.Width(it.summary) <= textW {
				l3 = padStyled(style.Render("")+it.summaryS, width, style.GetBackground())
			} else {
				l3 = paint(it.summary)
			}
		}
		return []string{l1, l2, l3}
	case stripChips:
		if it.summary != "" && it.count > 0 {
			summary := truncateWithEllipsis(it.summary, chipSummaryWidth)
			if !it.selected && lipgloss.Width(it.summary) <= chipSummaryWidth && lipgloss.Width(name)+3+lipgloss.Width(summary) <= textW {
				return []string{padStyled(style.Render(name+" · ")+it.summaryS, width, style.GetBackground())}
			}
			return []string{paint(name + " · " + summary)}
		}
		return []string{paint(name)}
	default:
		return []string{paint(name)}
	}
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
	// prio is the drop order when the table is too wide: higher goes
	// first; NAME and STATUS (prioKeep) never go.
	prio int
}

const (
	prioKeep    = 0
	prioWindow  = 1 // + column rank, so later windows go first
	prioLastUse = 100
)

// renderAccountsPane draws the selected provider's accounts in exactly
// height lines.
func (m Model) renderAccountsPane(g paneGeometry, height int) string {
	ps := m.profilesPanel.styles
	inner := g.inner
	muted := m.styles.StatusText
	provider := m.currentProvider()
	profiles := m.profilesPanel.profiles
	tier := m.tier()
	now := time.Now()

	// Title row: "<Provider> accounts" left, freshness right.
	left := ps.Title.MarginBottom(0).Render(providerLabel(provider) + " accounts")
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
	titleRow := ansi.Truncate(left+strings.Repeat(" ", gap)+right, inner, "…")

	frame := func(lines []string) string {
		return ps.Border.Width(g.pane).Height(height - 2).Render(fitWidth(strings.Join(lines, "\n"), inner))
	}
	lines := []string{titleRow}
	if len(profiles) == 0 {
		lines = append(lines, ps.Empty.Render(emptyProfilesMessage(provider)))
		return frame(lines)
	}

	// The header and its rule take two lines; a pane too short for that
	// and a row keeps the header and drops the rule.
	header := ps.Header
	headerLines := 2
	if height-2-1-2 < 1 {
		header = header.BorderBottom(false)
		headerLines = 1
	}
	tableRows := height - 2 - 1 - headerLines
	if tableRows < 1 {
		tableRows = 1
	}
	cols := m.accountColumns(provider, profiles, tier, inner, now)
	headerCells := make([]string, len(cols))
	for i, c := range cols {
		headerCells[i] = padRight(truncateWithEllipsis(c.header, c.width), c.width)
	}
	lines = append(lines, header.Render(padRight(strings.Join(headerCells, " "), inner)))

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
		blocks[i] = []string{padStyled(row, inner, rowStyle.GetBackground())}
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
	return frame(lines)
}

// accountColumns builds the table's columns for the tier and fits them to
// inner by dropping the least important ones: LAST USED first, then the
// per-model and longer windows from the right, then narrowing NAME.
func (m Model) accountColumns(provider string, profiles []ProfileInfo, tier layoutTier, inner int, now time.Time) []accountColumn {
	ps := m.profilesPanel.styles
	n := len(profiles)

	name := accountColumn{header: "NAME", cells: make([]string, n), styles: make([]lipgloss.Style, n), prio: prioKeep}
	status := accountColumn{header: "STATUS", cells: make([]string, n), styles: make([]lipgloss.Style, n), prio: prioKeep}
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
		if tier.allWindowColumns {
			for k, col := range m.windowColumnsFor(provider, profiles) {
				wc := accountColumn{header: col, window: col, cells: make([]string, n), styles: make([]lipgloss.Style, n), prio: prioWindow + k}
				for i, p := range profiles {
					wc.cells[i], wc.styles[i] = m.windowCell(provider, p.Name, col, tier, now)
				}
				cols = append(cols, wc)
			}
		} else {
			tight := accountColumn{header: "TIGHTEST", cells: make([]string, n), styles: make([]lipgloss.Style, n), prio: prioWindow}
			for i, p := range profiles {
				tight.cells[i], tight.styles[i] = m.tightestCell(provider, p.Name, now)
			}
			cols = append(cols, tight)
		}
	}
	if tier.showLastUsed {
		lu := accountColumn{header: "LAST USED", cells: make([]string, n), styles: make([]lipgloss.Style, n), prio: prioLastUse}
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
	if cols[0].width > maxNameWidth {
		cols[0].width = maxNameWidth
	}

	total := func() int {
		t := len(cols) - 1
		for _, c := range cols {
			t += c.width
		}
		return t
	}
	for total() > inner && len(cols) > 2 {
		drop, best := -1, prioKeep
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
	for total() > inner && cols[0].width > minNameWidth {
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
func (m Model) windowCell(provider, profile, column string, tier layoutTier, now time.Time) (string, lipgloss.Style) {
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
		if tier.longCells {
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
// windows where the row shows only the tightest, auth and token, where it
// lives, and what the keys do. Each line hangs off the row with a tree
// glyph; the last uses └.
func (m Model) expandedLines(provider string, info *ProfileInfo, inner int, tier layoutTier, now time.Time) []string {
	muted := m.styles.StatusText
	sep := muted.Render(" · ")
	statusStyle := m.profilesPanel.styles.StatusStyle
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

	if e, ok := m.limits[limitsKey(provider, info.Name)]; ok && m.hooks.Limits != nil {
		cells := usage.WindowsOf(e.info)
		switch {
		case !tier.allWindowColumns && len(cells) > 0:
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
		auth = append(auth, statusStyle(info.HealthStatus).Render(formatStatusLabel(info.HealthStatus)))
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
		auth = append(auth, statusStyle(info.HealthStatus).Render(formatStatusLabel(info.HealthStatus)))
	}
	items = append(items, strings.Join(auth, sep))

	if path := m.vaultPathFor(provider, info.Name); path != "" {
		items = append(items, muted.Render(path))
	}
	if !tier.showLastUsed {
		items = append(items, muted.Render("last used "+formatRelativeTime(info.LastUsed)))
	}

	// Actions, from the tier's list and one legend.
	legend := make([]string, 0, len(tier.actions))
	for _, k := range tier.actions {
		legend = append(legend, m.styles.StatusKey.Render(k)+muted.Render(" "+actionLegend[k]))
	}
	items = append(items, strings.Join(legend, "  "))

	lines := make([]string, len(items))
	for i, item := range items {
		glyph := "├─ "
		if i == len(items)-1 {
			glyph = "└─ "
		}
		lines[i] = ansi.Truncate("  "+muted.Render(glyph)+item, inner, "…")
	}
	return lines
}

// vaultPathFor is the profile's vault directory, shortened with ~ when it
// is under the default vault.
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

// padStyled pads an already-styled line to width in a background colour,
// so a highlight reaches the pane's edge. Only a colour is taken: a
// style's padding or margins would add their frame to the padding run.
func padStyled(s string, width int, bg lipgloss.TerminalColor) string {
	if pad := width - lipgloss.Width(s); pad > 0 {
		return s + lipgloss.NewStyle().Background(bg).Render(strings.Repeat(" ", pad))
	}
	return s
}

// fitWidth cuts every line of a block to width, keeping its styling, so a
// style with a fixed Width never soft-wraps it. A guard, not a layout
// step: lines are built to fit before they get here.
func fitWidth(block string, width int) string {
	lines := strings.Split(block, "\n")
	for i, line := range lines {
		if lipgloss.Width(line) > width {
			lines[i] = ansi.Truncate(line, width, "…")
		}
	}
	return strings.Join(lines, "\n")
}
