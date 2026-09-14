package monitor

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/usage"
)

// Refresher is what the dashboard polls: a Monitor, or a test double.
type Refresher interface {
	Refresh(ctx context.Context) error
	GetState() *MonitorState
}

// SwitchFunc makes profile the active profile for provider and returns a
// one-line summary of what happened. The dashboard calls it only after the
// user has confirmed the switch, and never while another switch is running.
type SwitchFunc func(ctx context.Context, provider, profile string) (string, error)

// DashboardOptions configures NewDashboard.
type DashboardOptions struct {
	// Interval between automatic refreshes. Zero means the monitor's default.
	Interval time.Duration
	// Switch performs a switch; nil makes the dashboard read-only.
	Switch SwitchFunc
	// Timeout bounds one refresh or switch. Zero means 60s.
	Timeout time.Duration
	// Now overrides the clock (tests).
	Now func() time.Time
}

// dashMode is the dashboard's input state.
type dashMode int

const (
	modeBrowse dashMode = iota
	modeConfirm
	modeSwitching
)

// dashRow is one profile line: the freshest usage the dashboard has for it,
// which is the latest fetch when that succeeded and the last good fetch
// (marked Stale) when it did not.
type dashRow struct {
	Provider string
	Name     string
	Active   bool
	Usage    *usage.UsageInfo
	// AsOf is when Usage was fetched.
	AsOf time.Time
	// Stale means Usage is a retained earlier result because the latest
	// fetch failed; Err holds that failure.
	Stale         bool
	Err           string
	InCooldown    bool
	CooldownUntil *time.Time
}

func (r dashRow) key() string { return profileKey(r.Provider, r.Name) }

type lastKnown struct {
	usage *usage.UsageInfo
	at    time.Time
}

// Dashboard is the interactive `caam monitor` screen: every captured account
// with each of its rate-limit windows as a column (percent left and the
// local reset time), the active account per provider starred, and Enter to
// switch the selected row after confirmation. It refreshes on a timer and on
// demand; refreshing only ever presents the access token it already has, so
// polling cannot rotate a refresh-token family.
type Dashboard struct {
	mon     Refresher
	opts    DashboardOptions
	now     func() time.Time
	rows    []dashRow
	columns []string
	cursor  int
	mode    dashMode

	width, height int

	refreshing  bool
	lastRefresh time.Time
	lastErr     string
	lastGood    map[string]lastKnown

	// message is the last outcome line (switch result, refresh error).
	message    string
	messageErr bool

	// pending is the row awaiting confirmation or being switched.
	pending *dashRow
}

// Messages.
type refreshDoneMsg struct {
	state *MonitorState
	err   error
}

type switchDoneMsg struct {
	provider, profile, summary string
	err                        error
}

type tickMsg time.Time

// NewDashboard builds the model. Call tea.NewProgram on it.
func NewDashboard(mon Refresher, opts DashboardOptions) *Dashboard {
	if opts.Interval <= 0 {
		opts.Interval = 30 * time.Second
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 60 * time.Second
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &Dashboard{
		mon:      mon,
		opts:     opts,
		now:      now,
		lastGood: make(map[string]lastKnown),
		width:    120,
		height:   40,
	}
}

// Init starts the first refresh and the refresh timer.
func (d *Dashboard) Init() tea.Cmd {
	return tea.Batch(d.refreshCmd(), d.tickCmd())
}

func (d *Dashboard) tickCmd() tea.Cmd {
	return tea.Tick(d.opts.Interval, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (d *Dashboard) refreshCmd() tea.Cmd {
	d.refreshing = true
	mon, timeout := d.mon, d.opts.Timeout
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		err := mon.Refresh(ctx)
		return refreshDoneMsg{state: mon.GetState(), err: err}
	}
}

func (d *Dashboard) switchCmd(row dashRow) tea.Cmd {
	sw, timeout := d.opts.Switch, d.opts.Timeout
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		summary, err := sw(ctx, row.Provider, row.Name)
		return switchDoneMsg{provider: row.Provider, profile: row.Name, summary: summary, err: err}
	}
}

// Update implements tea.Model.
func (d *Dashboard) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		d.width, d.height = msg.Width, msg.Height
		return d, nil

	case tickMsg:
		var cmd tea.Cmd
		if !d.refreshing && d.mode != modeSwitching {
			cmd = d.refreshCmd()
		}
		return d, tea.Batch(cmd, d.tickCmd())

	case refreshDoneMsg:
		d.refreshing = false
		d.lastRefresh = d.now()
		d.lastErr = ""
		if msg.err != nil {
			d.lastErr = msg.err.Error()
		}
		d.applyState(msg.state)
		return d, nil

	case switchDoneMsg:
		d.mode = modeBrowse
		d.pending = nil
		if msg.err != nil {
			d.message = fmt.Sprintf("switch %s -> %s failed: %v", msg.provider, msg.profile, msg.err)
			d.messageErr = true
			return d, nil
		}
		// The switch succeeded, so the star moves now; the refresh that
		// follows confirms it from the live state.
		for i := range d.rows {
			if d.rows[i].Provider == msg.provider {
				d.rows[i].Active = d.rows[i].Name == msg.profile
			}
		}
		d.message = msg.summary
		d.messageErr = false
		return d, d.refreshCmd()

	case tea.KeyMsg:
		return d.handleKey(msg)
	}
	return d, nil
}

func (d *Dashboard) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	if key == "ctrl+c" {
		return d, tea.Quit
	}

	switch d.mode {
	case modeConfirm:
		switch key {
		case "y", "Y", "enter":
			row := *d.pending
			d.mode = modeSwitching
			d.message = fmt.Sprintf("switching %s -> %s (re-capturing the outgoing account first)...", row.Provider, row.Name)
			d.messageErr = false
			return d, d.switchCmd(row)
		case "n", "N", "esc", "q":
			d.mode = modeBrowse
			d.pending = nil
			d.message = "switch cancelled"
			d.messageErr = false
		}
		return d, nil

	case modeSwitching:
		// Input is ignored until the switch reports back, so a second Enter
		// cannot start an overlapping switch.
		return d, nil
	}

	switch key {
	case "q", "esc":
		return d, tea.Quit
	case "up", "k":
		if d.cursor > 0 {
			d.cursor--
		}
	case "down", "j":
		if d.cursor < len(d.rows)-1 {
			d.cursor++
		}
	case "home", "g":
		d.cursor = 0
	case "end", "G":
		if len(d.rows) > 0 {
			d.cursor = len(d.rows) - 1
		}
	case "r":
		if !d.refreshing {
			return d, d.refreshCmd()
		}
	case "enter":
		if d.opts.Switch == nil {
			d.message = "switching is disabled in this session"
			d.messageErr = true
			return d, nil
		}
		if len(d.rows) == 0 {
			return d, nil
		}
		row := d.rows[d.cursor]
		if row.Active {
			d.message = fmt.Sprintf("%s/%s is already the active account", row.Provider, row.Name)
			d.messageErr = false
			return d, nil
		}
		if d.refreshing {
			d.message = "refresh in progress; try again in a moment"
			d.messageErr = true
			return d, nil
		}
		r := row
		d.pending = &r
		d.mode = modeConfirm
		d.message = ""
	}
	return d, nil
}

// applyState rebuilds the rows from a monitor snapshot, retaining the last
// good usage for a profile whose latest fetch failed so the screen keeps
// answering "how much is left" with an honest age instead of an error.
func (d *Dashboard) applyState(state *MonitorState) {
	if state == nil {
		return // no snapshot is no news: the rows stand
	}
	var selected string
	if d.cursor >= 0 && d.cursor < len(d.rows) {
		selected = d.rows[d.cursor].key()
	}

	rows := make([]dashRow, 0, len(state.Profiles))
	for _, key := range sortProfileKeys(state) {
		p := state.Profiles[key]
		if p == nil {
			continue
		}
		row := dashRow{
			Provider:      p.Provider,
			Name:          p.ProfileName,
			Active:        p.Active,
			Usage:         p.Usage,
			InCooldown:    p.InCooldown,
			CooldownUntil: p.CooldownUntil,
		}
		if p.Usage != nil {
			row.AsOf = p.Usage.FetchedAt
			row.Err = p.Usage.Error
		}
		if hasUsageData(p.Usage) {
			d.lastGood[key] = lastKnown{usage: p.Usage, at: fetchedAt(p.Usage, d.now())}
			row.AsOf = d.lastGood[key].at
		} else if prev, ok := d.lastGood[key]; ok {
			row.Usage = prev.usage
			row.AsOf = prev.at
			row.Stale = true
		}
		rows = append(rows, row)
	}
	d.rows = rows
	d.columns = windowColumns(rows)

	d.cursor = 0
	for i, r := range rows {
		if r.key() == selected {
			d.cursor = i
			break
		}
	}
}

func hasUsageData(u *usage.UsageInfo) bool {
	if u == nil || u.Error != "" {
		return false
	}
	return u.MostConstrainedWindow() != nil || u.Credits != nil
}

func fetchedAt(u *usage.UsageInfo, now time.Time) time.Time {
	if u != nil && !u.FetchedAt.IsZero() {
		return u.FetchedAt
	}
	return now
}

// windowColumns is the union of every row's window columns, in rank order.
func windowColumns(rows []dashRow) []string {
	ranks := make(map[string]int)
	for _, r := range rows {
		for _, c := range usage.WindowsOf(r.Usage) {
			if _, seen := ranks[c.Column]; !seen {
				ranks[c.Column] = c.Rank
			}
		}
	}
	cols := make([]string, 0, len(ranks))
	for name := range ranks {
		cols = append(cols, name)
	}
	sort.Slice(cols, func(i, j int) bool {
		if ranks[cols[i]] != ranks[cols[j]] {
			return ranks[cols[i]] < ranks[cols[j]]
		}
		return cols[i] < cols[j]
	})
	return cols
}

// rowStatus is the STATUS cell: the newest fact about the row that the
// window cells do not already say.
func rowStatus(r dashRow, now time.Time) string {
	switch {
	case r.Stale:
		return fmt.Sprintf("last known %s ago: %s", formatDuration(now.Sub(r.AsOf)), shortUsageError(r.Err))
	case r.Err != "" && r.Usage != nil && !hasUsageData(r.Usage):
		return shortUsageError(r.Err)
	case r.Usage == nil:
		return "no data"
	case r.InCooldown && r.CooldownUntil != nil:
		return "cooldown " + formatCooldown(r.CooldownUntil, now)
	}
	return "ok"
}

// Styles.
var (
	dashTitleStyle  = lipgloss.NewStyle().Bold(true)
	dashHeaderStyle = lipgloss.NewStyle().Bold(true).Underline(true)
	dashCursorStyle = lipgloss.NewStyle().Reverse(true)
	dashActiveStyle = lipgloss.NewStyle().Bold(true)
	dashDimStyle    = lipgloss.NewStyle().Faint(true)
	dashErrStyle    = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#b00020", Dark: "#ff6b6b"})
	dashOKStyle     = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#006d32", Dark: "#7ee787"})
)

// View implements tea.Model.
func (d *Dashboard) View() string {
	now := d.now()
	var b strings.Builder

	title := "caam monitor"
	if d.refreshing {
		title += "  (refreshing...)"
	} else if !d.lastRefresh.IsZero() {
		title += fmt.Sprintf("  refreshed %s, next %s", d.lastRefresh.Format("15:04:05"), d.lastRefresh.Add(d.opts.Interval).Format("15:04:05"))
	}
	b.WriteString(dashTitleStyle.Render(title))
	b.WriteString("\n\n")

	if len(d.rows) == 0 {
		if d.refreshing && d.lastRefresh.IsZero() {
			b.WriteString("Loading usage for every captured account...\n")
		} else {
			b.WriteString("No captured accounts with a usage API. Capture one with `caam backup <tool> <profile>`.\n")
		}
	} else {
		b.WriteString(d.renderTable(now))
	}

	b.WriteString("\n")
	switch d.mode {
	case modeConfirm:
		p := d.pending
		b.WriteString(fmt.Sprintf("Switch %s to %s?  [y] switch  [n] cancel\n", p.Provider, p.Name))
		b.WriteString(dashDimStyle.Render("The outgoing account is re-captured first. Running sessions keep their current login: codex until it restarts (or --reload-daemon), Claude Code until its next token refresh.") + "\n")
	case modeSwitching:
		b.WriteString(d.message + "\n")
	default:
		if d.message != "" {
			if d.messageErr {
				b.WriteString(dashErrStyle.Render(d.message) + "\n")
			} else {
				b.WriteString(dashOKStyle.Render(d.message) + "\n")
			}
		}
		if d.lastErr != "" {
			b.WriteString(dashErrStyle.Render("refresh: "+d.lastErr) + "\n")
		}
		b.WriteString(dashDimStyle.Render("up/down select   enter switch   r refresh   q quit     * = active account") + "\n")
	}
	return b.String()
}

// renderTable lays out the rows: PROFILE, one column per window, STATUS.
func (d *Dashboard) renderTable(now time.Time) string {
	headers := append([]string{"PROFILE"}, d.columns...)
	headers = append(headers, "STATUS")

	cells := make([][]string, len(d.rows))
	for i, r := range d.rows {
		byCol := make(map[string]string)
		for _, c := range usage.WindowsOf(r.Usage) {
			byCol[c.Column] = usage.WindowLeftText(c.Window, now)
		}
		line := make([]string, 0, len(headers))
		name := "  " + r.Provider + "/" + r.Name
		if r.Active {
			name = "* " + r.Provider + "/" + r.Name
		}
		line = append(line, name)
		for _, col := range d.columns {
			text, ok := byCol[col]
			if !ok {
				text = "-"
			}
			line = append(line, text)
		}
		line = append(line, rowStatus(r, now))
		cells[i] = line
	}

	widths := make([]int, len(headers))
	for i, h := range headers {
		widths[i] = len(h)
	}
	for _, line := range cells {
		for i, c := range line {
			if len(c) > widths[i] {
				widths[i] = len(c)
			}
		}
	}
	// Fit the terminal: shrink the widest columns first, never below the
	// header width, and let the row text truncate to what remains.
	const gap = 2
	total := func() int {
		t := gap * (len(widths) - 1)
		for _, w := range widths {
			t += w
		}
		return t
	}
	for total() > d.width && d.width > 0 {
		widest, at := 0, -1
		for i, w := range widths {
			if w > len(headers[i]) && w > widest {
				widest, at = w, i
			}
		}
		if at < 0 {
			break
		}
		widths[at]--
	}

	var b strings.Builder
	b.WriteString(dashHeaderStyle.Render(padRow(headers, widths, gap)) + "\n")
	for i, line := range cells {
		text := padRow(line, widths, gap)
		switch {
		case i == d.cursor && d.mode != modeSwitching:
			text = dashCursorStyle.Render(text)
		case d.rows[i].Active:
			text = dashActiveStyle.Render(text)
		case d.rows[i].Stale || (d.rows[i].Err != "" && !hasUsageData(d.rows[i].Usage)):
			text = dashDimStyle.Render(text)
		}
		b.WriteString(text + "\n")
	}
	return b.String()
}

func padRow(cells []string, widths []int, gap int) string {
	parts := make([]string, len(cells))
	for i, c := range cells {
		w := widths[i]
		if len(c) > w {
			if w > 3 {
				c = c[:w-3] + "..."
			} else {
				c = c[:w]
			}
		}
		parts[i] = c + strings.Repeat(" ", w-len(c))
	}
	return strings.TrimRight(strings.Join(parts, strings.Repeat(" ", gap)), " ")
}

// Rows exposes the current rows (tests and the non-interactive renderers).
func (d *Dashboard) Rows() []dashRow {
	out := make([]dashRow, len(d.rows))
	copy(out, d.rows)
	return out
}

// Columns exposes the window columns currently shown.
func (d *Dashboard) Columns() []string {
	return append([]string(nil), d.columns...)
}
