package usage

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// WindowCell is one rate-limit window with the names it is shown under:
// Column for a table header ("WEEKLY FABLE"), Label for a detail row
// ("Weekly Fable"), and Rank for left-to-right ordering (shorter windows
// first, per-model windows after the general window of the same length).
type WindowCell struct {
	Column string
	Label  string
	Rank   int
	Window *UsageWindow
}

// WindowsOf lists a profile's windows in display order: the general ones
// first (shortest to longest), then the per-model ones by name.
func WindowsOf(u *UsageInfo) []WindowCell {
	if u == nil {
		return nil
	}
	var cells []WindowCell
	seen := make(map[*UsageWindow]bool)
	add := func(w *UsageWindow, fallback string, slot int) {
		if w == nil || seen[w] {
			return
		}
		seen[w] = true
		column, label, rank := windowNames(w, fallback)
		if slot >= 0 && rank >= modelOnlyRank {
			// A slot window named only by its model (the agy pro/flash
			// picks) leads the per-model list rather than sorting into it.
			rank = modelOnlyRank - 5 + slot
		}
		cells = append(cells, WindowCell{Column: column, Label: label, Rank: rank, Window: w})
	}
	add(u.PrimaryWindow, "Primary", 0)
	add(u.SecondaryWindow, "Secondary", 1)
	add(u.TertiaryWindow, "Tertiary", 2)

	keys := make([]string, 0, len(u.ModelWindows))
	for key := range u.ModelWindows {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		w := u.ModelWindows[key]
		if w == nil {
			continue
		}
		if w.Label == "" {
			// Name the window after the map key when it carries no label.
			cp := *w
			cp.Label = key
			w = &cp
		}
		add(w, key, -1)
	}
	return cells
}

// modelOnlyRank is the rank of a window named by nothing but its model.
const modelOnlyRank = 60

// WindowColumn names the table column a window belongs in and its rank.
func WindowColumn(w *UsageWindow, fallback string) (string, int) {
	column, _, rank := windowNames(w, fallback)
	return column, rank
}

// windowNames derives a window's names from its duration when the provider
// reports one, else from its kind, else from the fallback; a per-model
// window gets the model appended.
func windowNames(w *UsageWindow, fallback string) (column, label string, rank int) {
	base := ""
	switch d := w.WindowDuration; {
	case d == 5*time.Hour:
		base, rank = "5-hour", 10
	case d == 24*time.Hour:
		base, rank = "Daily", 20
	case d == 7*24*time.Hour:
		base, rank = "Weekly", 30
	case d == 30*24*time.Hour:
		base, rank = "Monthly", 40
	case d > 0:
		base, rank = formatWindowLength(d), 50
	}
	if base == "" {
		switch w.Kind {
		case "session":
			base, rank = "5-hour", 10
		case "weekly_all", "weekly_scoped":
			base, rank = "Weekly", 30
		}
	}
	switch {
	case w.Label != "" && base == "":
		return strings.ToUpper(w.Label), w.Label, modelOnlyRank
	case w.Label != "":
		return strings.ToUpper(base + " " + w.Label), base + " " + w.Label, rank + 5
	case base == "":
		return strings.ToUpper(fallback), fallback, 70
	}
	return strings.ToUpper(base), base, rank
}

func formatWindowLength(d time.Duration) string {
	switch {
	case d%(24*time.Hour) == 0:
		return fmt.Sprintf("%dd", int(d.Hours())/24)
	case d%time.Hour == 0:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return d.String()
	}
}

// PercentLeft is the share of a window still available, clamped to 0-100.
func PercentLeft(w *UsageWindow) int {
	if w == nil {
		return 0
	}
	used := w.UsedPercent
	if used == 0 && w.Utilization > 0 {
		used = int(w.Utilization*100 + 0.5)
	}
	left := 100 - used
	if left < 0 {
		left = 0
	}
	if left > 100 {
		left = 100
	}
	return left
}

// WindowLeftText renders a window as the share left and when it resets, in
// the viewer's local time: "82% left, resets 6:10 PM" today, "resets Tue
// 5:00 PM" later this week, "resets Sep 20 8:45 AM" beyond that.
func WindowLeftText(w *UsageWindow, now time.Time) string {
	if w == nil {
		return "-"
	}
	if w.Rolled {
		return "100% left (reset)"
	}
	left := PercentLeft(w)
	if w.ResetsAt.IsZero() {
		return fmt.Sprintf("%d%% left", left)
	}
	return fmt.Sprintf("%d%% left, resets %s", left, LocalReset(w.ResetsAt, now))
}

// LocalReset renders a reset instant in now's location, dropping the day
// when it is today and using the weekday within the next six days.
func LocalReset(at, now time.Time) string {
	at = at.In(now.Location())
	if at.Before(now) {
		return "now"
	}
	clock := strings.TrimPrefix(at.Format("3:04 PM"), "0")
	if at.Year() == now.Year() && at.YearDay() == now.YearDay() {
		return clock
	}
	if at.Sub(now) < 6*24*time.Hour {
		return at.Format("Mon ") + clock
	}
	return at.Format("Jan 2 ") + clock
}
