package cmd

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/usage"
)

// updateGolden rewrites the golden files under testdata from the current
// output: go test ./cmd/caam/cmd -run Golden -update
var updateGolden = flag.Bool("update", false, "rewrite golden files")

// limitsFixtureRows is a fixed set of limits rows: every time is pinned in
// UTC so the JSON contract can be compared byte for byte.
func limitsFixtureRows() []usage.ProfileUsage {
	fetched := time.Date(2026, 9, 13, 18, 0, 0, 0, time.UTC)
	return []usage.ProfileUsage{
		{
			Provider: "claude", ProfileName: "work",
			Usage: &usage.UsageInfo{
				Provider: "claude", ProfileName: "work", PlanType: "max",
				PrimaryWindow: &usage.UsageWindow{
					Utilization: 0.12, UsedPercent: 12, Kind: "session",
					WindowDuration: 5 * time.Hour,
					ResetsAt:       time.Date(2026, 9, 13, 20, 50, 0, 0, time.UTC),
				},
				SecondaryWindow: &usage.UsageWindow{
					Utilization: 0.5, UsedPercent: 50, Kind: "weekly_all",
					WindowDuration: 7 * 24 * time.Hour,
					ResetsAt:       time.Date(2026, 9, 16, 17, 0, 0, 0, time.UTC),
				},
				ModelWindows: map[string]*usage.UsageWindow{
					"fable": {Utilization: 0.9, UsedPercent: 90, Label: "Fable", Kind: "weekly_scoped",
						WindowDuration: 7 * 24 * time.Hour},
				},
				Source: usage.SourceAPI, FetchedAt: fetched,
				BurnRate: &usage.BurnRateInfo{TokensPerHour: 12000},
			},
		},
		{
			Provider: "codex", ProfileName: "personal",
			Usage: &usage.UsageInfo{
				Provider: "codex", ProfileName: "personal", PlanType: "plus",
				PrimaryWindow: &usage.UsageWindow{
					Utilization: 0.4, UsedPercent: 40, WindowDuration: 5 * time.Hour,
					ResetsAt: time.Date(2026, 9, 13, 21, 15, 0, 0, time.UTC),
				},
				SecondaryWindow: &usage.UsageWindow{
					Utilization: 0.7, UsedPercent: 70, WindowDuration: 7 * 24 * time.Hour,
					ResetsAt: time.Date(2026, 9, 18, 9, 0, 0, 0, time.UTC),
				},
				Credits: &usage.CreditInfo{HasCredits: true},
				Source:  usage.SourceAPI, FetchedAt: fetched,
			},
		},
		{
			Provider: "codex", ProfileName: "broken",
			Usage: &usage.UsageInfo{Provider: "codex", ProfileName: "broken", Error: "boom", FetchedAt: fetched},
		},
	}
}

// TestLimitsJSON_Golden pins `caam limits --format json` byte for byte: the
// table's vocabulary may change (left, reset clock) but the JSON is a
// contract (used_percent, resets_at) that machine callers read.
func TestLimitsJSON_Golden(t *testing.T) {
	var b strings.Builder
	if err := renderLimits(&b, "json", limitsFixtureRows(), time.Date(2026, 9, 13, 18, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("renderLimits json: %v", err)
	}
	got := b.String()

	golden := filepath.Join("testdata", "limits_json.golden")
	if *updateGolden {
		if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("read golden (run with -update to create it): %v", err)
	}
	if got != string(want) {
		t.Errorf("limits --format json drifted from the golden contract\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

// TestLimitsTable_SaysLeftAndResetClock: the table reads as the dashboard
// does — the provider by its product name, one column per window, each
// cell the share left and the local clock it resets at — with STATUS as
// before. Used-side numbers and durations belong to JSON only.
func TestLimitsTable_SaysLeftAndResetClock(t *testing.T) {
	now := time.Date(2026, 9, 13, 18, 0, 0, 0, time.UTC)
	rows := limitsFixtureRows()

	var b strings.Builder
	if err := renderLimits(&b, "table", rows, now); err != nil {
		t.Fatalf("renderLimits table: %v", err)
	}
	out := b.String()

	for _, want := range []string{
		"AGENT", "PROFILE", "5-HOUR", "WEEKLY", "WEEKLY FABLE", "STATUS",
		"Claude Code", "work",
		"88% left · " + usage.LocalReset(rows[0].Usage.PrimaryWindow.ResetsAt, now),
		"50% left · " + usage.LocalReset(rows[0].Usage.SecondaryWindow.ResetsAt, now),
		"10% left",
		"Codex", "personal",
		"60% left · " + usage.LocalReset(rows[1].Usage.PrimaryWindow.ResetsAt, now),
		"30% left · " + usage.LocalReset(rows[1].Usage.SecondaryWindow.ResetsAt, now),
		"broken", "error: boom",
		" ok",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("limits table lacks %q:\n%s", want, out)
		}
	}
	for _, gone := range []string{"PRIMARY", "SECONDARY", "SCOPED", "RESETS IN", "12%", "40%", "70%", "claude/work"} {
		if strings.Contains(out, gone) {
			t.Errorf("limits table still says %q (used-side or raw id):\n%s", gone, out)
		}
	}

	// A row without a window the table has a column for shows a dash there.
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "personal") && !strings.Contains(line, "  -  ") {
			t.Errorf("codex row should show - under WEEKLY FABLE:\n%s", line)
		}
	}
}

// TestLimitsRankTable_SaysLeft: --rank speaks of headroom as what is left
// and names the reset as a local clock, like the table it sits next to.
func TestLimitsRankTable_SaysLeft(t *testing.T) {
	now := time.Date(2026, 9, 13, 18, 0, 0, 0, time.UTC)
	resets := time.Date(2026, 9, 13, 20, 50, 0, 0, time.UTC)
	secs := int64(resets.Sub(now).Seconds())
	top := usage.RankedProfile{Provider: "claude", Profile: "work", Rank: 1, Eligible: true,
		Tier: usage.TierIncludedHeadroom, Reason: "resets soonest", UsedPercent: 12, HeadroomPercent: 88,
		ResetsAt: &resets, ResetsInSeconds: &secs}
	result := &usage.RankResult{
		Rank: usage.RankEarliestResetHeadroom, HeadroomCeiling: 80,
		Selected: &top,
		Profiles: []usage.RankedProfile{
			top,
			{Provider: "claude", Profile: "spent", Eligible: false, Tier: usage.TierExhausted,
				Reason: "no headroom", UsedPercent: 100, HeadroomPercent: 0},
		},
	}

	var b strings.Builder
	if err := renderRankTable(&b, result, now); err != nil {
		t.Fatalf("renderRankTable: %v", err)
	}
	out := b.String()
	for _, want := range []string{"LEFT", "RESETS", "88% left", usage.LocalReset(resets, now), "0% left", "Claude Code"} {
		if !strings.Contains(out, want) {
			t.Errorf("rank table lacks %q:\n%s", want, out)
		}
	}
	for _, gone := range []string{"USED", "RESETS IN", "12%", "claude/work"} {
		if strings.Contains(out, gone) {
			t.Errorf("rank table still says %q:\n%s", gone, out)
		}
	}
}

// TestLimitsForecast_SaysLeft: --forecast reports each window as what is
// left, not what is used.
func TestLimitsForecast_SaysLeft(t *testing.T) {
	rows := limitsFixtureRows()
	// Make the resets relative to the real clock so the durations are positive.
	rows[0].Usage.PrimaryWindow.ResetsAt = time.Now().Add(2 * time.Hour)
	rows[0].Usage.SecondaryWindow.ResetsAt = time.Now().Add(72 * time.Hour)

	var b strings.Builder
	if err := renderForecast(&b, "table", rows[:1]); err != nil {
		t.Fatalf("renderForecast: %v", err)
	}
	out := b.String()
	for _, want := range []string{"Claude Code", "work", "88% left", "50% left"} {
		if !strings.Contains(out, want) {
			t.Errorf("forecast lacks %q:\n%s", want, out)
		}
	}
	for _, gone := range []string{"12%", "claude/work"} {
		if strings.Contains(out, gone) {
			t.Errorf("forecast still says %q:\n%s", gone, out)
		}
	}
}
