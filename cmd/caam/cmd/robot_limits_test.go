package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	caamdb "github.com/Dicklesworthstone/coding_agent_account_manager/internal/db"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/usage"
	"github.com/spf13/cobra"
)

// robotLimitsResponse mirrors the RobotOutput wrapper for the limits
// command so a test can assert on the decoded payload.
type robotLimitsResponse struct {
	Success bool            `json:"success"`
	Command string          `json:"command"`
	Data    RobotLimitsData `json:"data"`
	Error   *RobotError     `json:"error"`
}

type robotNextResponse struct {
	Success bool          `json:"success"`
	Data    RobotNextData `json:"data"`
	Error   *RobotError   `json:"error"`
}

func newRobotNextCmd(strategy string) *cobra.Command {
	cmd := &cobra.Command{}
	cmd.Flags().String("strategy", strategy, "")
	cmd.Flags().Bool("include-cooldown", false, "")
	return cmd
}

// TestRobotLimits_ReturnsLimits: `caam robot limits` promised usage and
// reset times and returned a health guess. It now reads each account's
// limits the way the dashboard does (fetchProfileLimits) and reports what
// is left, when it resets, and when the account was last used.
func TestRobotLimits_ReturnsLimits(t *testing.T) {
	_, cleanup := setupHistoryTestEnv(t)
	defer cleanup()
	for _, dir := range []string{"codex/alpha", "codex/beta"} {
		if err := os.MkdirAll(vault.ProfilePath("codex", filepath.Base(dir)), 0o700); err != nil {
			t.Fatal(err)
		}
	}

	resets := time.Date(2026, 9, 13, 20, 50, 0, 0, time.UTC)
	used := time.Now().Add(-3 * time.Hour).Truncate(time.Second)
	db, err := caamdb.Open()
	if err != nil {
		t.Fatal(err)
	}
	if err := db.LogEvent(caamdb.Event{Type: caamdb.EventActivate, Provider: "codex", ProfileName: "alpha", Timestamp: used}); err != nil {
		t.Fatal(err)
	}
	db.Close()

	oldFetch := robotFetchLimits
	t.Cleanup(func() { robotFetchLimits = oldFetch })
	var fetched []string
	robotFetchLimits = func(ctx context.Context, provider, profile string) (*usage.UsageInfo, error) {
		fetched = append(fetched, provider+"/"+profile)
		if profile == "beta" {
			return nil, errors.New("401 unauthorized")
		}
		return &usage.UsageInfo{
			Provider: provider, ProfileName: profile,
			PrimaryWindow:   &usage.UsageWindow{UsedPercent: 12, WindowDuration: 5 * time.Hour, ResetsAt: resets},
			SecondaryWindow: &usage.UsageWindow{UsedPercent: 60, WindowDuration: 7 * 24 * time.Hour, ResetsAt: resets.Add(72 * time.Hour)},
			FetchedAt:       time.Now(),
		}, nil
	}

	cmd := &cobra.Command{}
	cmd.Flags().Bool("forecast", false, "")
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	if err := runRobotLimits(cmd, []string{"codex"}); err != nil {
		t.Fatalf("runRobotLimits: %v\n%s", err, buf.String())
	}
	var resp robotLimitsResponse
	if err := json.Unmarshal(buf.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v\n%s", err, buf.String())
	}
	if !resp.Success || resp.Data.Provider != "codex" || len(resp.Data.Profiles) != 2 {
		t.Fatalf("unexpected payload: %s", buf.String())
	}
	if len(fetched) != 2 {
		t.Errorf("limits fetched for %v, want both profiles", fetched)
	}

	byName := map[string]RobotProfileLimits{}
	for _, p := range resp.Data.Profiles {
		byName[p.Name] = p
	}
	alpha := byName["alpha"]
	// The tightest window (weekly, 40% left) is what a controller must plan on.
	if alpha.LeftPercent != 40 {
		t.Errorf("alpha left_percent = %d, want 40 (the tightest window)", alpha.LeftPercent)
	}
	if alpha.ResetsAt != resets.Add(72*time.Hour).Format(time.RFC3339) {
		t.Errorf("alpha resets_at = %q, want the tightest window's reset", alpha.ResetsAt)
	}
	if alpha.LastUsed != used.Format(time.RFC3339) {
		t.Errorf("alpha last_used = %q, want %s", alpha.LastUsed, used.Format(time.RFC3339))
	}
	if alpha.Error != "" || alpha.AvailScore == 0 {
		t.Errorf("alpha should carry a score and no error: %+v", alpha)
	}

	beta := byName["beta"]
	if beta.Error == "" || beta.LeftPercent != 0 || beta.LastUsed != "" {
		t.Errorf("beta should report the fetch error and nothing else: %+v", beta)
	}
}

// TestRobotNext_LRUUsesTheActivityLog: `caam robot next --strategy lru`
// prefers the account the activity log saw used longest ago, not merely
// one that is not active.
func TestRobotNext_LRUUsesTheActivityLog(t *testing.T) {
	_, cleanup := setupHistoryTestEnv(t)
	defer cleanup()
	for _, dir := range []string{"codex/fresh", "codex/stale"} {
		if err := os.MkdirAll(vault.ProfilePath("codex", filepath.Base(dir)), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	db, err := caamdb.Open()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for _, ev := range []caamdb.Event{
		{Type: caamdb.EventActivate, Provider: "codex", ProfileName: "fresh", Timestamp: now.Add(-time.Hour)},
		{Type: caamdb.EventActivate, Provider: "codex", ProfileName: "stale", Timestamp: now.Add(-5 * 24 * time.Hour)},
	} {
		if err := db.LogEvent(ev); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()

	cmd := newRobotNextCmd("lru")
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	if err := runRobotNext(cmd, []string{"codex"}); err != nil {
		t.Fatalf("runRobotNext: %v\n%s", err, buf.String())
	}
	var resp robotNextResponse
	if err := json.Unmarshal(buf.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v\n%s", err, buf.String())
	}
	if resp.Data.Profile != "stale" {
		t.Errorf("lru picked %q, want stale (last used 5d ago):\n%s", resp.Data.Profile, buf.String())
	}
	joined := ""
	for _, r := range resp.Data.Reasons {
		joined += r + "; "
	}
	if !bytes.Contains([]byte(joined), []byte("last used")) {
		t.Errorf("reasons should say when it was last used: %v", resp.Data.Reasons)
	}
}
