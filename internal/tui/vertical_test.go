package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// TestProviderStripNeverWrapsInsideItsPane: a row of cards even one column
// wider than the pane is soft-wrapped by the border style, which
// interleaved the second row of cards with the first at 159 columns. The
// strip's height must be exactly what its content needs, and no line of
// the card grid may exceed the pane's inner width, at any width, with any
// provider selected.
func TestProviderStripNeverWrapsInsideItsPane(t *testing.T) {
	for width := 100; width <= 200; width++ {
		m := modelWithLimits(t, width, 42)
		m.profiles["codex"] = []Profile{{Name: "ops@example.com", Provider: "codex", IsActive: true}}
		m.profiles["gemini"] = []Profile{{Name: "g@example.com", Provider: "gemini", IsActive: true}}
		m.profiles["agy"] = []Profile{{Name: "a@example.com", Provider: "agy", IsActive: true}}
		m.profiles["zcode"] = []Profile{{Name: "z@example.com", Provider: "zcode", IsActive: true}}
		for step := 0; step < len(m.providers); step++ {
			m.syncProfilesPanel()
			inner := width - 5
			if m.widthTier() == tierWide {
				grid := m.renderProviderCards(inner)
				for i, line := range strings.Split(grid, "\n") {
					if w := lipgloss.Width(line); w > inner {
						t.Fatalf("width %d, provider %d: card row %d is %d wide, pane inner is %d:\n%s", width, step, i, w, inner, stripANSI(line))
					}
				}
				cards := 0
				for _, c := range m.providerCards() {
					if c.count > 0 || c.selected {
						cards++
					}
				}
				perRow := (inner + 1) / (providerCardWidth + 1)
				wantRows := (cards + perRow - 1) / perRow
				wantLines := 3*wantRows + 1 // + the not-logged-in line
				if got := lipgloss.Height(grid); got != wantLines {
					t.Fatalf("width %d, provider %d: grid is %d lines, want %d (%d cards, %d per row):\n%s", width, step, got, wantLines, cards, perRow, stripANSI(grid))
				}
			}
			strip := m.renderProviderStrip(inner)
			for i, line := range strings.Split(strip, "\n") {
				if w := lipgloss.Width(line); w > width-1 {
					t.Fatalf("width %d, provider %d: strip line %d is %d wide", width, step, i, w)
				}
			}
			updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
			m = updated.(Model)
		}
	}
}
