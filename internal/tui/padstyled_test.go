package tui

import (
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// TestPadStyledIgnoresTheStyleFrame: padding a line with a style that has
// its own padding used to add that padding again, so every provider card
// came out a column wider than its slot and a full row soft-wrapped inside
// the pane. The padded line is exactly the requested width.
func TestPadStyledIgnoresTheStyleFrame(t *testing.T) {
	framed := lipgloss.NewStyle().PaddingLeft(1).MarginRight(2).Background(lipgloss.Color("#dbeafe"))
	for _, text := range []string{"", "x", "● csells@sellsbrothers.com", "▸ Kimi Code (0)"} {
		line := framed.Render(text)
		if got := lipgloss.Width(padStyled(line, 30, framed.GetBackground())); got != 30 {
			t.Errorf("padStyled(%q) = %d wide, want 30", text, got)
		}
	}
	// A line already at or past the width is returned untouched.
	long := framed.Render("a line that is already wider than the requested thirty columns")
	if padStyled(long, 30, framed.GetBackground()) != long {
		t.Errorf("padStyled changed a line that needed no padding")
	}
}
