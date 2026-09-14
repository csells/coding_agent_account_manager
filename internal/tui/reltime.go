package tui

import (
	"fmt"
	"time"
)

// FormatRelativeTime says how long ago t was, as of now, in the words the
// dashboard uses: never, now, 5m ago, 3h ago, 2d ago, 1w ago, 2mo ago. It
// is the one ladder for "last used" everywhere caam shows it.
func FormatRelativeTime(t, now time.Time) string {
	if t.IsZero() {
		return "never"
	}
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 7*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%dw ago", int(d.Hours()/(24*7)))
	default:
		months := int(d.Hours() / (24 * 30))
		if months == 0 {
			months = 1
		}
		return fmt.Sprintf("%dmo ago", months)
	}
}
