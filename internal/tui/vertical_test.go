package tui

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/usage"
)

// nineProviders is every provider caam manages with an account captured
// for each, in key order — the widest strip the dashboard can show.
func nineProviders(t *testing.T, w, h int) Model {
	t.Helper()
	m := modelWithLimits(t, w, h)
	for _, p := range []string{"codex", "gemini", "grok", "opencode", "cursor", "agy", "kimi", "zcode"} {
		m.profiles[p] = []Profile{{Name: p + "@example.com", Provider: p, IsActive: true}}
	}
	m.syncProfilesPanel()
	m.settleStrip()
	return m
}

// stripLines returns the provider strip's rendered lines, plain.
func stripLines(m Model) []string {
	return strings.Split(ansi.Strip(m.renderProviderStrip(paneGeom(m.width))), "\n")
}

// slotColumn is the column a provider's label starts at in the strip, or
// -1 when it is not on screen.
func slotColumn(m Model, label string) int {
	for _, line := range stripLines(m) {
		if i := strings.Index(line, label+" "); i >= 0 {
			return lipgloss.Width(line[:i]) // columns, not bytes: │ and ▸ are multi-byte
		}
	}
	return -1
}

func TestProviderStrip_SlotsKeepTheirPlaceAsTheSelectionMoves(t *testing.T) {
	m := nineProviders(t, 159, 42)
	// Every provider visible at first has a fixed column, selected or not.
	before := map[string]int{}
	for _, id := range m.providers {
		before[id] = slotColumn(m, providerLabel(id))
	}
	if before["claude"] != before["codex"] && before["codex"] < 0 {
		t.Fatalf("codex not on screen at start:\n%s", strings.Join(stripLines(m), "\n"))
	}
	firstOffset := m.stripOffset
	for step := 0; step < len(m.providers); step++ {
		for _, id := range m.providers {
			if col := slotColumn(m, providerLabel(id)); col >= 0 && before[id] >= 0 && m.stripOffset == firstOffset && col != before[id] {
				t.Fatalf("step %d: %s moved from column %d to %d:\n%s", step, id, before[id], col, strings.Join(stripLines(m), "\n"))
			}
		}
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRight})
		m = updated.(Model)
	}
}

func TestProviderStrip_ScrollsOnlyWhenTheSelectionLeavesTheWindow(t *testing.T) {
	m := nineProviders(t, 159, 42)
	kind, items, inner := m.stripKind(), m.stripItems(), paneGeom(m.width).inner
	prevOffset := m.stripOffset
	for step := 0; step < 2*len(m.providers); step++ {
		offset, count := m.stripWindow(kind, items, inner)
		sel := m.activeProvider
		if sel < offset || sel >= offset+count {
			t.Fatalf("step %d: selection %d outside window [%d,%d)", step, sel, offset, offset+count)
		}
		lines := stripLines(m)
		joined := strings.Join(lines, "\n")
		if offset > 0 && !strings.Contains(joined, "‹ "+strconv.Itoa(offset)+" ") {
			t.Errorf("step %d: %d hidden on the left, no ‹ indicator:\n%s", step, offset, joined)
		}
		if rest := len(items) - offset - count; rest > 0 && !strings.Contains(joined, " "+strconv.Itoa(rest)+" ›") {
			t.Errorf("step %d: %d hidden on the right, no › indicator:\n%s", step, rest, joined)
		}
		if offset == 0 && strings.Contains(joined, "‹") {
			t.Errorf("step %d: ‹ shown with nothing hidden on the left", step)
		}
		// The window moved only if the selection had left it.
		if offset != prevOffset && !(sel == offset || sel == offset+count-1) {
			t.Errorf("step %d: window jumped to %d with the selection at %d", step, offset, sel)
		}
		prevOffset = offset
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRight})
		m = updated.(Model)
	}
}

func TestProviderStrip_FitsEveryWidthAndKeepsTheHighlight(t *testing.T) {
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(prev) })

	for _, h := range []int{12, 24, 42} {
		for w := 40; w <= 200; w += 3 {
			m := nineProviders(t, w, h)
			bg := sgrBackground(t, m.stripStyles.SelectedItem)
			for step := 0; step < len(m.providers); step++ {
				g := paneGeom(m.width)
				strip := m.renderProviderStrip(g)
				want := m.stripKind().lines() + 2
				if m.stripKind() == stripCards {
					want++
				}
				if got := lipgloss.Height(strip); got != want {
					t.Fatalf("%dx%d step %d: strip is %d lines, want %d", w, h, step, got, want)
				}
				for i, line := range strings.Split(strip, "\n") {
					if lw := lipgloss.Width(line); lw > w {
						t.Fatalf("%dx%d step %d: strip line %d is %d wide", w, h, step, i, lw)
					}
				}
				label := providerLabel(m.providers[m.activeProvider])
				if !strings.Contains(strip, bg) || !strings.Contains(ansi.Strip(strip), "▸ "+label) {
					t.Fatalf("%dx%d step %d: selected %s not highlighted:\n%s", w, h, step, label, ansi.Strip(strip))
				}
				updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRight})
				m = updated.(Model)
			}
		}
	}
}

func TestProviderStrip_CardsFillTheRow(t *testing.T) {
	m := nineProviders(t, 159, 42)
	_, count := m.stripWindow(stripCards, m.stripItems(), paneGeom(m.width).inner)
	if count != 5 {
		t.Fatalf("159 columns should show 5 cards, got %d:\n%s", count, strings.Join(stripLines(m), "\n"))
	}
	if w := cardWidth(155, 9); w < minCardWidth || w > providerCardWidth {
		t.Fatalf("cardWidth(155, 9) = %d, outside [%d, %d]", w, minCardWidth, providerCardWidth)
	}
}

// A provider with no captured account has no place on the strip: there
// is nothing to switch between. n is how such a provider gets its first
// account.
func TestProviderStrip_HidesProvidersWithoutAnAccount(t *testing.T) {
	m := modelWithLimits(t, 159, 42)
	for _, p := range []string{"codex", "gemini", "agy", "zcode"} {
		m.profiles[p] = []Profile{{Name: p + "@example.com", Provider: p, IsActive: true}}
	}
	m.syncProfilesPanel()
	joined := strings.Join(stripLines(m), "\n")
	if !strings.Contains(joined, "Providers (5)") {
		t.Errorf("five providers have accounts:\n%s", joined)
	}
	for _, id := range []string{"grok", "opencode", "cursor", "kimi"} {
		if strings.Contains(joined, providerLabel(id)) {
			t.Errorf("%s has no account and should not be on the strip:\n%s", id, joined)
		}
	}
	if len(m.providers) != 5 || len(m.allProviders) != 9 {
		t.Fatalf("providers = %v (all %v)", m.providers, m.allProviders)
	}
	// ←/→ only visit providers with accounts.
	seen := map[string]bool{}
	for i := 0; i < 5; i++ {
		seen[m.currentProvider()] = true
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRight})
		m = updated.(Model)
	}
	if len(seen) != 5 || seen["grok"] {
		t.Fatalf("→ visited %v", seen)
	}
}

// Capturing a provider's first account puts it on the strip, selected.
func TestProviderStrip_NewProviderAppearsWhenItGetsAnAccount(t *testing.T) {
	m := modelWithLimits(t, 159, 42)
	if strings.Contains(strings.Join(stripLines(m), "\n"), "Kimi") {
		t.Fatal("kimi should not be on the strip before it has an account")
	}
	m.profiles["kimi"] = []Profile{{Name: "k@example.com", Provider: "kimi", IsActive: true}}
	m.selectedProfileName = "k@example.com"
	m.activeProvider = 1 // stale index from before the sync: kimi is not visible yet
	m.syncProfilesPanel()
	joined := strings.Join(stripLines(m), "\n")
	if !strings.Contains(joined, "Kimi Code (1)") || !strings.Contains(joined, "Providers (2)") {
		t.Fatalf("kimi should join the strip once it has an account:\n%s", joined)
	}
}

func TestAccountsPaneIsExactlyItsHeight(t *testing.T) {
	m := nineProviders(t, 159, 42)
	g := paneGeom(m.width)
	for _, h := range []int{6, 8, 10, 12, 20, 30} {
		if got := lipgloss.Height(m.renderAccountsPane(g, h)); got != h {
			t.Errorf("pane asked for %d lines, got %d", h, got)
		}
	}
	// And the pane's bottom border survives the frame clamp.
	view := ansi.Strip(m.View())
	lines := strings.Split(view, "\n")
	if !strings.HasPrefix(strings.TrimSpace(lines[len(lines)-2]), "╰") {
		t.Errorf("accounts pane lost its bottom border:\n%s", view)
	}
}

func TestShortTerminalKeepsTheAccountsPane(t *testing.T) {
	for _, size := range [][2]int{{100, 12}, {80, 12}, {120, 16}, {159, 20}} {
		m := nineProviders(t, size[0], size[1])
		view := ansi.Strip(m.View())
		if lipgloss.Height(m.View()) > size[1] {
			t.Errorf("%dx%d: view is %d lines", size[0], size[1], lipgloss.Height(m.View()))
		}
		for _, want := range []string{"Claude Code accounts", "NAME", "a@example.com"} {
			if !strings.Contains(view, want) {
				t.Errorf("%dx%d: view lacks %q:\n%s", size[0], size[1], want, view)
			}
		}
		lines := strings.Split(view, "\n")
		if !strings.Contains(lines[len(lines)-1], "CLAUDE") {
			t.Errorf("%dx%d: status bar is not the last line:\n%s", size[0], size[1], view)
		}
		if !strings.HasPrefix(strings.TrimSpace(lines[len(lines)-2]), "╰") {
			t.Errorf("%dx%d: accounts pane lost its bottom border:\n%s", size[0], size[1], view)
		}
	}
}

func TestCtrlCQuitsEvenWithTheCardOpen(t *testing.T) {
	m := modelWithLimits(t, 170, 40)
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("i")})
	m = updated.(Model)
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatalf("ctrl+c with the card open did not quit")
	}
	if msg := cmd(); msg == nil {
		t.Fatalf("ctrl+c produced no quit message")
	} else if _, ok := msg.(tea.QuitMsg); !ok {
		t.Fatalf("ctrl+c produced %T, want tea.QuitMsg", msg)
	}
}

func TestFailedLimitsAreNotRefetchedOnEveryKey(t *testing.T) {
	rec := &limitsRecorder{err: errors.New("unauthorized: token expired")}
	m := modelWithTwoClaudeProfiles(Hooks{Limits: rec.fetch})
	m.width, m.height = 170, 40
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = runCmd(t, updated.(Model), cmd)
	calls := rec.calls["claude/b@example.com"]
	for i := 0; i < 5; i++ {
		updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyUp})
		m = runCmd(t, updated.(Model), cmd)
		updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyDown})
		m = runCmd(t, updated.(Model), cmd)
	}
	if rec.calls["claude/b@example.com"] != calls {
		t.Fatalf("a failed fetch was retried on keypresses: %d -> %d", calls, rec.calls["claude/b@example.com"])
	}
}

func TestSearchKeysDoNotFetchLimits(t *testing.T) {
	rec := &limitsRecorder{info: sampleLimits()}
	m := modelWithTwoClaudeProfiles(Hooks{Limits: rec.fetch})
	m.width, m.height = 170, 40
	m.state = stateSearch
	for _, r := range "abc" {
		_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		if cmd != nil {
			if _, isBatch := cmd().(tea.BatchMsg); isBatch {
				t.Fatalf("typing %q into search started a limits fetch", string(r))
			}
		}
	}
	if len(rec.calls) != 0 {
		t.Fatalf("search keys fetched limits: %v", rec.calls)
	}
}

var _ = context.Background
var _ = time.Now
var _ = usage.PercentLeft

// modelWithLimits is a two-account Claude model whose limits are already
// loaded, at the given terminal size.
func modelWithLimits(t *testing.T, w, h int) Model {
	t.Helper()
	rec := &limitsRecorder{info: sampleLimits()}
	m := modelWithTwoClaudeProfiles(Hooks{Limits: rec.fetch})
	m.width, m.height = w, h
	for _, name := range []string{"a@example.com", "b@example.com"} {
		m.applyLimitsLoaded(limitsLoadedMsg{provider: "claude", profile: name, info: sampleLimits()})
	}
	return m
}

func TestVerticalLayout_ProvidersAboveAccountsWithWindowColumns(t *testing.T) {
	m := modelWithLimits(t, 170, 40)
	view := ansi.Strip(m.View())

	iProviders := strings.Index(view, "Providers")
	iAccounts := strings.Index(view, "Claude Code accounts")
	if iProviders < 0 || iAccounts < 0 || iProviders > iAccounts {
		t.Fatalf("providers strip must sit above the accounts pane:\n%s", view)
	}
	for _, want := range []string{"▸ Claude Code (2)", "● a@example.com", "5h 82%", "wk 50%", "Fable 10%", "Providers (1)",
		"NAME", "STATUS", "5-HOUR", "WEEKLY", "WEEKLY FABLE", "LAST USED",
		"82% left · ", "50% left · ", "10% left", "enter", "switch to this account"} {
		if !strings.Contains(view, want) {
			t.Errorf("wide view lacks %q:\n%s", want, view)
		}
	}
	if lipgloss.Height(m.View()) > 40 {
		t.Errorf("view taller than the terminal: %d", lipgloss.Height(m.View()))
	}
}

func TestVerticalLayout_SelectedAccountExpandsInPlace(t *testing.T) {
	m := modelWithLimits(t, 170, 40)
	m.profiles["claude"] = append(m.profiles["claude"], Profile{Name: "c@example.com", Provider: "claude"})
	m.syncProfilesPanel()
	m.applyLimitsLoaded(limitsLoadedMsg{provider: "claude", profile: "c@example.com", info: sampleLimits()})

	// The first account is selected: its tree hangs between it and the
	// second row; the others are single rows.
	view := ansi.Strip(m.View())
	iA, iTree, iB, iC := strings.Index(view, "a@example.com"), strings.Index(view, "├─"), strings.Index(view, "  b@example.com"), strings.Index(view, "  c@example.com")
	if !(iA < iTree && iTree < iB && iB < iC) {
		t.Fatalf("expansion must sit under the selected row:\n%s", view)
	}
	if strings.Count(view, "└─") != 1 {
		t.Fatalf("exactly one expansion should be open:\n%s", view)
	}
	for _, want := range []string{"├─ oauth", "└─", "switch to this account", "~/vault/claude/a@example.com"} {
		if !strings.Contains(view, want) {
			t.Errorf("expansion lacks %q:\n%s", want, view)
		}
	}

	// ↓ moves the expansion to the next account.
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(Model)
	view = ansi.Strip(m.View())
	iA, iB, iTree = strings.Index(view, "  a@example.com"), strings.Index(view, "b@example.com"), strings.Index(view, "├─")
	if !(iA < iB && iB < iTree) || !strings.Contains(view, "~/vault/claude/b@example.com") {
		t.Fatalf("expansion did not follow the selection:\n%s", view)
	}
}

func TestVerticalLayout_ScrollsByAccountKeepingTheExpansionVisible(t *testing.T) {
	m := modelWithLimits(t, 170, 24) // few rows: header (2) + strip (8) + pane
	for _, n := range []string{"c", "d", "e", "f", "g", "h"} {
		m.profiles["claude"] = append(m.profiles["claude"], Profile{Name: n + "@example.com", Provider: "claude"})
	}
	m.syncProfilesPanel()
	for i := 0; i < 7; i++ {
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
		m = updated.(Model)
	}
	view := ansi.Strip(m.View())
	if !strings.Contains(view, "h@example.com") || !strings.Contains(view, "└─") {
		t.Fatalf("last account and its expansion must be in view:\n%s", view)
	}
	if lipgloss.Height(m.View()) > 24 {
		t.Fatalf("view taller than the terminal: %d", lipgloss.Height(m.View()))
	}
}

func TestVerticalLayout_MediumDropsLastUsedAndShortensCells(t *testing.T) {
	m := modelWithLimits(t, 120, 30)
	view := ansi.Strip(m.View())
	if strings.Contains(view, "LAST USED") {
		t.Errorf("medium tier should drop LAST USED:\n%s", view)
	}
	if !strings.Contains(view, "82% · ") || strings.Contains(view, "82% left") {
		t.Errorf("medium tier should show short window cells:\n%s", view)
	}
	if !strings.Contains(view, "▸ Claude Code 2") {
		t.Errorf("medium tier should show one-line provider chips:\n%s", view)
	}
	for _, line := range strings.Split(m.View(), "\n") {
		if lipgloss.Width(line) > 120 {
			t.Fatalf("line wider than the terminal (%d): %q", lipgloss.Width(line), ansi.Strip(line))
		}
	}
}

func TestVerticalLayout_NarrowShowsTightestWindowAndTabRow(t *testing.T) {
	m := modelWithLimits(t, 80, 24)
	view := ansi.Strip(m.View())
	for _, want := range []string{"TIGHTEST", "Fable 10%", "▸ Claude Code 2", "├─ 5-hour", "82% left", "├─ Weekly"} {
		if !strings.Contains(view, want) {
			t.Errorf("narrow view lacks %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "5-HOUR") {
		t.Errorf("narrow tier should not show every window column:\n%s", view)
	}
	for _, line := range strings.Split(m.View(), "\n") {
		if lipgloss.Width(line) > 80 {
			t.Fatalf("line wider than the terminal (%d): %q", lipgloss.Width(line), ansi.Strip(line))
		}
	}
	if lipgloss.Height(m.View()) > 24 {
		t.Errorf("view taller than the terminal: %d", lipgloss.Height(m.View()))
	}
}

func TestVerticalLayout_ArrowsSteerProvidersThenAccounts(t *testing.T) {
	m := modelWithLimits(t, 170, 40)
	m.profiles["codex"] = []Profile{{Name: "c@example.com", Provider: "codex", IsActive: true}}
	m.syncProfilesPanel()

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRight})
	m = updated.(Model)
	if m.currentProvider() != "codex" || !strings.Contains(ansi.Strip(m.View()), "Codex accounts") {
		t.Fatalf("→ should select the next provider's accounts, got %q", m.currentProvider())
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyLeft})
	m = updated.(Model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(Model)
	if m.currentProvider() != "claude" || m.selectedProfileInfo().Name != "b@example.com" {
		t.Fatalf("← then ↓ should select claude's second account, got %s/%v", m.currentProvider(), m.selectedProfileInfo())
	}
}

func TestVerticalLayout_DetailCardOverlayToggles(t *testing.T) {
	m := modelWithLimits(t, 170, 40)
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("i")})
	m = updated.(Model)
	if !m.showDetailCard || !strings.Contains(ansi.Strip(m.View()), "Profile: a@example.com") {
		t.Fatalf("i should open the full card for the selected account")
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(Model)
	if !m.showDetailCard || !strings.Contains(ansi.Strip(m.View()), "Profile: b@example.com") {
		t.Fatalf("↓ under the card should move it to the next account")
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(Model)
	if m.showDetailCard {
		t.Fatalf("esc should close the card")
	}
}

func TestVerticalLayout_PrefetchCoversTableAndStrip(t *testing.T) {
	rec := &limitsRecorder{info: sampleLimits()}
	m := modelWithTwoClaudeProfiles(Hooks{Limits: rec.fetch})
	m.profiles["codex"] = []Profile{{Name: "c@example.com", Provider: "codex", IsActive: true}, {Name: "d@example.com", Provider: "codex"}}
	m.syncProfilesPanel()

	cmd := m.limitsPrefetchCmd()
	if cmd == nil {
		t.Fatalf("prefetch returned nothing")
	}
	runCmd(t, m, cmd)
	// Both Claude accounts (the table) and Codex's active account (the
	// strip), but not Codex's idle one.
	for _, want := range []string{"claude/a@example.com", "claude/b@example.com", "codex/c@example.com"} {
		if rec.calls[want] != 1 {
			t.Errorf("%s fetched %d times, want 1 (calls=%v)", want, rec.calls[want], rec.calls)
		}
	}
	if rec.calls["codex/d@example.com"] != 0 {
		t.Errorf("idle account of another provider was fetched: %v", rec.calls)
	}
}

// TestVerticalLayout_NoLineExceedsTheTerminal: every line of the view
// fits the terminal, at every tier, with the selection anywhere — a line
// that overflows is soft-wrapped by the pane's border style and corrupts
// every row beneath it.
func TestVerticalLayout_NoLineExceedsTheTerminal(t *testing.T) {
	for _, size := range [][2]int{{170, 40}, {159, 42}, {150, 40}, {120, 30}, {100, 24}, {80, 24}, {60, 20}} {
		m := nineProviders(t, size[0], size[1])
		for step := 0; step < len(m.providers); step++ {
			for i, line := range strings.Split(m.View(), "\n") {
				if w := lipgloss.Width(line); w > size[0] {
					t.Fatalf("%dx%d, provider %d, line %d is %d wide: %q", size[0], size[1], step, i, w, ansi.Strip(line))
				}
			}
			if h := lipgloss.Height(m.View()); h > size[1] {
				t.Fatalf("%dx%d, provider %d: view is %d lines", size[0], size[1], step, h)
			}
			updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
			m = updated.(Model)
		}
	}
}

// A per-model provider can report more windows than the table has columns
// for. The columns that do not fit are dropped from the table, and the
// expansion lists exactly those under the selected account, so nothing the
// API reported is unreachable.
func TestVerticalLayout_ExpansionListsTheWindowsWithoutAColumn(t *testing.T) {
	info := &usage.UsageInfo{Provider: "agy", ModelWindows: map[string]*usage.UsageWindow{}}
	for i := 0; i < 12; i++ {
		name := fmt.Sprintf("gemini-model-%02d", i)
		info.ModelWindows[name] = &usage.UsageWindow{Label: name, Kind: "model_quota", UsedPercent: i}
	}
	info.PrimaryWindow = info.ModelWindows["gemini-model-11"]
	m := modelWithTwoClaudeProfiles(Hooks{Limits: (&limitsRecorder{info: info}).fetch})
	m.width, m.height = 160, 40
	for _, name := range []string{"a@example.com", "b@example.com"} {
		m.applyLimitsLoaded(limitsLoadedMsg{provider: "claude", profile: name, info: info})
	}
	view := ansi.Strip(m.View())

	// The primary window leads the columns even though it sorts last by name.
	iPrimary, iFirst := strings.Index(view, "GEMINI-MODEL-11"), strings.Index(view, "GEMINI-MODEL-00")
	if iPrimary < 0 || (iFirst >= 0 && iFirst < iPrimary) {
		t.Fatalf("the primary window should be the first window column:\n%s", view)
	}
	inTable, inTree := 0, 0
	for i := 0; i < 12; i++ {
		name := fmt.Sprintf("gemini-model-%02d", i)
		if strings.Contains(view, strings.ToUpper(name)) {
			inTable++
		}
		if strings.Contains(view, "├─ "+name) || strings.Contains(view, "└─ "+name) {
			inTree++
		}
	}
	if inTable == 12 || inTable == 0 {
		t.Fatalf("expected some but not all windows to fit as columns, got %d:\n%s", inTable, view)
	}
	if inTable+inTree != 12 {
		t.Fatalf("columns (%d) + tree lines (%d) must cover every window:\n%s", inTable, inTree, view)
	}
	for _, line := range strings.Split(m.View(), "\n") {
		if lipgloss.Width(line) > 160 {
			t.Fatalf("line wider than the terminal (%d): %q", lipgloss.Width(line), ansi.Strip(line))
		}
	}
}

// LAST USED comes from the activity log's last use of the account, carried
// on the vault metadata; upstream's isolated-profile store, which nothing
// writes, is only consulted first.
func TestAccountsPane_LastUsedComesFromTheActivityLog(t *testing.T) {
	m := modelWithLimits(t, 170, 40)
	m.vaultMeta = map[string]map[string]vaultProfileMeta{
		"claude": {"a@example.com": {LastUsed: time.Now().Add(-3 * time.Hour)}},
	}
	m.syncProfilesPanel()
	view := ansi.Strip(m.View())
	if !strings.Contains(view, "3h ago") {
		t.Fatalf("a@example.com was used 3h ago:\n%s", view)
	}
	if !strings.Contains(view, "never") {
		t.Fatalf("b@example.com has no use on record and should say never:\n%s", view)
	}
}
