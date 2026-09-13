package usage

import (
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
