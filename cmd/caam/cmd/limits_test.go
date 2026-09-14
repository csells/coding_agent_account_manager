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
			Usage:    &usage.UsageInfo{Provider: "codex", ProfileName: "broken", Error: "boom", FetchedAt: fetched},
		},
	}
}

// TestLimitsJSON_Golden pins `caam limits --format json` byte for byte: the
// table's vocabulary may change (left, reset clock) but the JSON is a
// contract (used_percent, resets_at) that machine callers read.
func TestLimitsJSON_Golden(t *testing.T) {
	var b strings.Builder
	if err := renderLimits(&b, "json", limitsFixtureRows(), ""); err != nil {
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
