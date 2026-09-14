package usage

import (
	"strings"
	"testing"
	"time"
)

func TestWindowColumn_NamesByDurationThenKindThenLabel(t *testing.T) {
	cases := []struct {
		w    UsageWindow
		want string
	}{
		{UsageWindow{WindowDuration: 5 * time.Hour}, "5-HOUR"},
		{UsageWindow{WindowDuration: 7 * 24 * time.Hour}, "WEEKLY"},
		{UsageWindow{WindowDuration: 7 * 24 * time.Hour, Label: "Fable"}, "WEEKLY FABLE"},
		{UsageWindow{WindowDuration: 30 * 24 * time.Hour}, "MONTHLY"},
		{UsageWindow{Kind: "session"}, "5-HOUR"},
		{UsageWindow{Kind: "weekly_scoped", Label: "Opus"}, "WEEKLY OPUS"},
		{UsageWindow{Label: "gemini-2.5-pro"}, "GEMINI-2.5-PRO"},
		{UsageWindow{}, "PRIMARY"},
		{UsageWindow{WindowDuration: 3 * 24 * time.Hour}, "3D"},
	}
	for _, c := range cases {
		got, _ := WindowColumn(&c.w, "Primary")
		if got != c.want {
			t.Errorf("WindowColumn(%+v) = %q, want %q", c.w, got, c.want)
		}
	}
}

func TestWindowsOf_OrdersGeneralThenPerModelWithLabels(t *testing.T) {
	u := &UsageInfo{
		PrimaryWindow:   &UsageWindow{WindowDuration: 5 * time.Hour},
		SecondaryWindow: &UsageWindow{WindowDuration: 7 * 24 * time.Hour},
		ModelWindows: map[string]*UsageWindow{
			"Opus":  {WindowDuration: 7 * 24 * time.Hour, Label: "Opus"},
			"Fable": {WindowDuration: 7 * 24 * time.Hour},
		},
	}
	cells := WindowsOf(u)
	var labels []string
	for _, c := range cells {
		labels = append(labels, c.Label)
	}
	want := []string{"5-hour", "Weekly", "Weekly Fable", "Weekly Opus"}
	if len(labels) != len(want) {
		t.Fatalf("labels = %v, want %v", labels, want)
	}
	for i := range want {
		if labels[i] != want[i] {
			t.Fatalf("labels = %v, want %v", labels, want)
		}
	}
	if cells[0].Rank >= cells[1].Rank || cells[1].Rank >= cells[2].Rank {
		t.Fatalf("ranks not increasing: %+v", cells)
	}
}

func TestWindowLeftText_LocalResetPhrasing(t *testing.T) {
	loc := time.FixedZone("PDT", -7*3600)
	now := time.Date(2026, 9, 13, 13, 40, 0, 0, loc)
	cases := []struct {
		w    UsageWindow
		want string
	}{
		{UsageWindow{UsedPercent: 18, ResetsAt: now.Add(4*time.Hour + 30*time.Minute)}, "82% left, resets 6:10 PM"},
		{UsageWindow{UsedPercent: 50, ResetsAt: time.Date(2026, 9, 15, 17, 0, 0, 0, loc)}, "50% left, resets Tue 5:00 PM"},
		{UsageWindow{UsedPercent: 70, ResetsAt: time.Date(2026, 9, 20, 8, 45, 0, 0, loc)}, "30% left, resets Sep 20 8:45 AM"},
		{UsageWindow{Utilization: 0.9}, "10% left"},
		{UsageWindow{UsedPercent: 130, ResetsAt: now.Add(time.Hour)}, "0% left, resets 2:40 PM"},
		{UsageWindow{UsedPercent: 40, Rolled: true}, "100% left (reset)"},
		{UsageWindow{UsedPercent: 40, ResetsAt: now.Add(-time.Minute)}, "60% left, resets now"},
	}
	for _, c := range cases {
		if got := WindowLeftText(&c.w, now); got != c.want {
			t.Errorf("WindowLeftText(%+v) = %q, want %q", c.w, got, c.want)
		}
	}
	if WindowLeftText(nil, now) != "-" {
		t.Errorf("nil window should render as -")
	}
}

// LeftText and ResetText are the two halves of that sentence, one per
// table column: what is left of the window, and the local clock it resets
// at. A window with nothing to say in a column shows "-" there.
func TestLeftTextAndResetText_AreTheTwoTableColumns(t *testing.T) {
	loc := time.FixedZone("PDT", -7*3600)
	now := time.Date(2026, 9, 13, 13, 40, 0, 0, loc)
	cases := []struct {
		w         UsageWindow
		left, rst string
	}{
		{UsageWindow{UsedPercent: 12, ResetsAt: now.Add(4*time.Hour + 30*time.Minute)}, "88% left", "6:10 PM"},
		{UsageWindow{UsedPercent: 50, ResetsAt: time.Date(2026, 9, 15, 17, 0, 0, 0, loc)}, "50% left", "Tue 5:00 PM"},
		{UsageWindow{UsedPercent: 70, ResetsAt: time.Date(2026, 9, 20, 8, 45, 0, 0, loc)}, "30% left", "Sep 20 8:45 AM"},
		{UsageWindow{Utilization: 0.9}, "10% left", "-"},
		{UsageWindow{UsedPercent: 130, ResetsAt: now.Add(time.Hour)}, "0% left", "2:40 PM"},
		{UsageWindow{UsedPercent: 40, Rolled: true}, "100% left (reset)", "-"},
		{UsageWindow{UsedPercent: 40, ResetsAt: now.Add(-time.Minute)}, "60% left", "now"},
	}
	for _, c := range cases {
		if got := LeftText(&c.w); got != c.left {
			t.Errorf("LeftText(%+v) = %q, want %q", c.w, got, c.left)
		}
		if got := ResetText(&c.w, now); got != c.rst {
			t.Errorf("ResetText(%+v) = %q, want %q", c.w, got, c.rst)
		}
	}
	if LeftText(nil) != "-" || ResetText(nil, now) != "-" {
		t.Errorf("nil window should render as - in both columns")
	}
}

// Per-model providers (agy) name every window by its model. The pro and
// flash picks are the primary and secondary windows: they lead the list,
// once each, ahead of the alphabetical rest.
func TestWindowsOf_ModelSlotsLeadOnceAndTheRestFollowByName(t *testing.T) {
	pro := &UsageWindow{Label: "gemini-3-pro", Kind: "model_quota"}
	flash := &UsageWindow{Label: "gemini-3-flash", Kind: "model_quota"}
	u := &UsageInfo{
		PrimaryWindow:   pro,
		SecondaryWindow: flash,
		ModelWindows: map[string]*UsageWindow{
			"claude-sonnet-4-6":   {Label: "claude-sonnet-4-6", Kind: "model_quota"},
			"gemini-3-flash":      flash,
			"gemini-3-flash-lite": {Label: "gemini-3-flash-lite", Kind: "model_quota"},
			"gemini-3-pro":        pro,
		},
	}
	var got []string
	for _, c := range WindowsOf(u) {
		got = append(got, c.Label)
	}
	want := []string{"gemini-3-pro", "gemini-3-flash", "claude-sonnet-4-6", "gemini-3-flash-lite"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("labels = %v, want %v", got, want)
	}
	cells := WindowsOf(u)
	if !(cells[0].Rank < cells[1].Rank && cells[1].Rank < cells[2].Rank && cells[2].Rank == cells[3].Rank) {
		t.Fatalf("ranks = %d %d %d %d, want the slots ahead of an equal-ranked rest", cells[0].Rank, cells[1].Rank, cells[2].Rank, cells[3].Rank)
	}
}
