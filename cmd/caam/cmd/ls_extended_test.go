package cmd

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	caamdb "github.com/Dicklesworthstone/coding_agent_account_manager/internal/db"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/provider"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLs_IsOrderedLabelledAndDated: `caam ls` lists providers in the
// dashboard strip's order, titles each by its product name, and says when
// each account was last used — from the activity log, as the dashboard
// row does — with last_used in --json.
func TestLs_IsOrderedLabelledAndDated(t *testing.T) {
	originalVault := vault
	defer func() { vault = originalVault }()
	vaultDir := t.TempDir()
	vault = authfile.NewVault(vaultDir)
	for _, dir := range []string{"kimi/k1", "claude/work", "codex/p1"} {
		require.NoError(t, os.MkdirAll(filepath.Join(vaultDir, dir), 0o755))
	}

	// The activity log saw claude/work switched to three hours ago.
	used := time.Now().Add(-3 * time.Hour).Truncate(time.Second)
	db, err := caamdb.Open()
	require.NoError(t, err)
	require.NoError(t, db.LogEvent(caamdb.Event{Type: caamdb.EventActivate, Provider: "claude", ProfileName: "work", Timestamp: used}))
	require.NoError(t, db.Close())

	t.Cleanup(func() { _ = lsCmd.Flags().Set("json", "false") })
	require.NoError(t, lsCmd.Flags().Set("json", "false"))
	out, err := captureStdout(t, func() error { return runLs(lsCmd, nil) })
	require.NoError(t, err)

	// Strip order (claude, codex, ... kimi), titled by label.
	want := []string{"Claude Code:", "Codex:", "Kimi Code:"}
	last := -1
	for _, title := range want {
		i := strings.Index(out, title)
		if i < 0 {
			t.Fatalf("ls lacks the title %q:\n%s", title, out)
		}
		if i < last {
			t.Errorf("ls lists %q out of strip order:\n%s", title, out)
		}
		last = i
	}
	if strings.Contains(out, "claude:") || strings.Contains(out, "kimi:") {
		t.Errorf("ls still titles a provider by its raw id:\n%s", out)
	}
	// The order is the strip's, not the map's or the alphabet's; a tool the
	// strip does not know follows, alphabetically.
	var strip []string
	for _, id := range provider.DisplayOrder() {
		if id == "claude" || id == "codex" || id == "kimi" {
			strip = append(strip, id)
		}
	}
	assert.Equal(t, append(strip, "zz-new"), lsToolOrder(map[string][]string{
		"kimi": {"k1"}, "zz-new": {"x"}, "codex": {"p1"}, "claude": {"work"},
	}))

	if !strings.Contains(out, "LAST USED") {
		t.Errorf("ls lacks a LAST USED column:\n%s", out)
	}
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.Contains(line, "work") && !strings.Contains(line, "3h ago"):
			t.Errorf("claude/work should read 3h ago: %q", line)
		case strings.Contains(line, "k1") && !strings.Contains(line, "never"):
			t.Errorf("kimi/k1 should read never: %q", line)
		}
	}

	// --json carries the same instant.
	require.NoError(t, lsCmd.Flags().Set("json", "true"))
	jsonOut, err := captureStdout(t, func() error { return runLs(lsCmd, nil) })
	require.NoError(t, err)
	var payload lsOutput
	require.NoError(t, json.Unmarshal([]byte(jsonOut), &payload), jsonOut)
	for _, p := range payload.Profiles {
		switch p.Tool + "/" + p.Name {
		case "claude/work":
			got, perr := time.Parse(time.RFC3339, p.LastUsed)
			require.NoError(t, perr, "last_used should be RFC3339: %q", p.LastUsed)
			assert.True(t, got.Equal(used), "last_used = %s, want %s", got, used)
		case "kimi/k1":
			assert.Empty(t, p.LastUsed, "an account never used has no last_used")
		}
	}
}

func TestLsCommand_Extended(t *testing.T) {
	h := testutil.NewExtendedHarness(t)
	defer h.Close()

	// 1. Setup
	h.StartStep("Setup", "Create vault with profiles")
	
	// Override global vault
	originalVault := vault
	originalTools := make(map[string]func() authfile.AuthFileSet)
	for k, v := range tools {
		originalTools[k] = v
	}
	defer func() {
		vault = originalVault
		tools = originalTools
	}()
	
	vaultDir := filepath.Join(h.TempDir, "vault")
	vault = authfile.NewVault(vaultDir)
	
	// Create some profiles manually in the vault structure
	// Claude profiles
	require.NoError(t, os.MkdirAll(filepath.Join(vaultDir, "claude", "work"), 0755))
	require.NoError(t, os.MkdirAll(filepath.Join(vaultDir, "claude", "personal"), 0755))
	
	// Codex profiles
	require.NoError(t, os.MkdirAll(filepath.Join(vaultDir, "codex", "project-x"), 0755))
	
	// We need to override tools map so lsCmd knows about these tools
	// and can check active profile (which requires fileSet)
	tools["claude"] = func() authfile.AuthFileSet {
		return authfile.AuthFileSet{Tool: "claude", Files: []authfile.AuthFileSpec{}}
	}
	tools["codex"] = func() authfile.AuthFileSet {
		return authfile.AuthFileSet{Tool: "codex", Files: []authfile.AuthFileSpec{}}
	}
	tools["gemini"] = func() authfile.AuthFileSet {
		return authfile.AuthFileSet{Tool: "gemini", Files: []authfile.AuthFileSpec{}}
	}
	h.EndStep("Setup")
	
	// 2. Execute ls --json
	h.StartStep("Execute", "Run ls --json")
	
	// Capture stdout
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	
	// Reset flags
	lsCmd.Flags().Set("json", "true")
	lsCmd.Flags().Set("tag", "")
	lsCmd.Flags().Set("no-color", "false")
	
	// Run
	err := runLs(lsCmd, []string{})
	
	// Restore stdout
	w.Close()
	os.Stdout = oldStdout
	
	require.NoError(t, err)
	
	// Read output
	var buf bytes.Buffer
	io.Copy(&buf, r)
	outputJSON := buf.String()
	h.LogDebug("ls output", "json", outputJSON)
	h.EndStep("Execute")
	
	// 3. Verify
	h.StartStep("Verify", "Check JSON output")
	
	var output lsOutput
	err = json.Unmarshal([]byte(outputJSON), &output)
	require.NoError(t, err)
	
	assert.Equal(t, 3, output.Count)
	
	// Check profiles exist in output
	foundWork := false
	foundPersonal := false
	foundProjectX := false
	
	for _, p := range output.Profiles {
		if p.Tool == "claude" && p.Name == "work" { foundWork = true }
		if p.Tool == "claude" && p.Name == "personal" { foundPersonal = true }
		if p.Tool == "codex" && p.Name == "project-x" { foundProjectX = true }
	}
	
	assert.True(t, foundWork, "claude/work not found")
	assert.True(t, foundPersonal, "claude/personal not found")
	assert.True(t, foundProjectX, "codex/project-x not found")
	
	h.EndStep("Verify")
}

// TestLsCommand_FilterByTool tests `ls claude`
func TestLsCommand_FilterByTool(t *testing.T) {
	h := testutil.NewExtendedHarness(t)
	defer h.Close()

	vaultDir := filepath.Join(h.TempDir, "vault")
	vault = authfile.NewVault(vaultDir)
	
	require.NoError(t, os.MkdirAll(filepath.Join(vaultDir, "claude", "work"), 0755))
	require.NoError(t, os.MkdirAll(filepath.Join(vaultDir, "codex", "project-x"), 0755))
	
	// Reset flags
	lsCmd.Flags().Set("json", "true")
	
	// Capture stdout
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	
	err := runLs(lsCmd, []string{"claude"})
	
	w.Close()
	os.Stdout = oldStdout
	require.NoError(t, err)
	
	var buf bytes.Buffer
	io.Copy(&buf, r)
	
	var output lsOutput
	json.Unmarshal(buf.Bytes(), &output)
	
	assert.Equal(t, 1, output.Count)
	assert.Equal(t, "claude", output.Profiles[0].Tool)
	assert.Equal(t, "work", output.Profiles[0].Name)
}
