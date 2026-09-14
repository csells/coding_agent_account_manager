package cmd

import (
	"encoding/json"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/testutil"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
)

func TestActivate_AutoBackupsOriginalOnFirstSwitch(t *testing.T) {
	tmpDir := t.TempDir()

	// Isolate Codex auth location.
	oldCodexHome := os.Getenv("CODEX_HOME")
	t.Cleanup(func() { _ = os.Setenv("CODEX_HOME", oldCodexHome) })
	_ = os.Setenv("CODEX_HOME", filepath.Join(tmpDir, "codex_home"))

	if err := os.MkdirAll(os.Getenv("CODEX_HOME"), 0700); err != nil {
		t.Fatalf("MkdirAll(CODEX_HOME) error = %v", err)
	}

	original := []byte(`{"access_token":"original","token_type":"Bearer"}`)
	originalAuthPath := filepath.Join(os.Getenv("CODEX_HOME"), "auth.json")
	if err := os.WriteFile(originalAuthPath, original, 0600); err != nil {
		t.Fatalf("WriteFile(original auth) error = %v", err)
	}

	// Use a temp vault.
	oldVault := vault
	vault = authfile.NewVault(filepath.Join(tmpDir, "vault"))
	t.Cleanup(func() { vault = oldVault })

	// Create a target profile in the vault with different contents.
	targetProfileDir := vault.ProfilePath("codex", "target")
	if err := os.MkdirAll(targetProfileDir, 0700); err != nil {
		t.Fatalf("MkdirAll(target profile) error = %v", err)
	}
	target := []byte(`{"access_token":"target","token_type":"Bearer"}`)
	if err := os.WriteFile(filepath.Join(targetProfileDir, "auth.json"), target, 0600); err != nil {
		t.Fatalf("WriteFile(target auth) error = %v", err)
	}

	if err := runActivate(activateCmd, []string{"codex", "target"}); err != nil {
		t.Fatalf("runActivate() error = %v", err)
	}

	// Ensure the original state was preserved as a system `_original` profile.
	origBackupPath := vault.BackupPath("codex", "_original", "auth.json")
	gotOriginal, err := os.ReadFile(origBackupPath)
	if err != nil {
		t.Fatalf("ReadFile(_original auth) error = %v", err)
	}
	if string(gotOriginal) != string(original) {
		t.Fatalf("_original auth mismatch: got %q want %q", gotOriginal, original)
	}

	// Ensure metadata marks it as a system "first-activate" backup.
	metaRaw, err := os.ReadFile(filepath.Join(vault.ProfilePath("codex", "_original"), "meta.json"))
	if err != nil {
		t.Fatalf("ReadFile(_original meta.json) error = %v", err)
	}
	var meta struct {
		Type          string   `json:"type"`
		CreatedBy     string   `json:"created_by"`
		OriginalPaths []string `json:"original_paths"`
	}
	if err := json.Unmarshal(metaRaw, &meta); err != nil {
		t.Fatalf("Unmarshal(_original meta.json) error = %v", err)
	}
	if meta.Type != "system" {
		t.Fatalf("meta.type = %q, want %q", meta.Type, "system")
	}
	if meta.CreatedBy != "first-activate" {
		t.Fatalf("meta.created_by = %q, want %q", meta.CreatedBy, "first-activate")
	}
	if len(meta.OriginalPaths) != 1 || meta.OriginalPaths[0] != originalAuthPath {
		t.Fatalf("meta.original_paths = %v, want [%s]", meta.OriginalPaths, originalAuthPath)
	}

	// Ensure activation actually switched the auth file.
	gotActive, err := os.ReadFile(originalAuthPath)
	if err != nil {
		t.Fatalf("ReadFile(active auth) error = %v", err)
	}
	if string(gotActive) != string(target) {
		t.Fatalf("active auth mismatch: got %q want %q", gotActive, target)
	}
}

func TestActivate_AutoBackupsUnsavedStateBeforeSwitch(t *testing.T) {
	tmpDir := t.TempDir()

	// Isolate Codex auth location.
	oldCodexHome := os.Getenv("CODEX_HOME")
	t.Cleanup(func() { _ = os.Setenv("CODEX_HOME", oldCodexHome) })
	_ = os.Setenv("CODEX_HOME", filepath.Join(tmpDir, "codex_home"))

	// Isolate CAAM_HOME (SPM config lives here).
	oldCaamHome := os.Getenv("CAAM_HOME")
	t.Cleanup(func() { _ = os.Setenv("CAAM_HOME", oldCaamHome) })
	_ = os.Setenv("CAAM_HOME", filepath.Join(tmpDir, "caam_home"))

	if err := os.MkdirAll(os.Getenv("CODEX_HOME"), 0700); err != nil {
		t.Fatalf("MkdirAll(CODEX_HOME) error = %v", err)
	}

	unsaved := []byte(`{"access_token":"unsaved","token_type":"Bearer"}`)
	authPath := filepath.Join(os.Getenv("CODEX_HOME"), "auth.json")
	if err := os.WriteFile(authPath, unsaved, 0600); err != nil {
		t.Fatalf("WriteFile(current auth) error = %v", err)
	}

	// Configure safety settings to keep only 1 auto-backup.
	if err := os.MkdirAll(os.Getenv("CAAM_HOME"), 0700); err != nil {
		t.Fatalf("MkdirAll(CAAM_HOME) error = %v", err)
	}
	spmCfg := []byte("version: 1\nsafety:\n  auto_backup_before_switch: smart\n  max_auto_backups: 1\n")
	if err := os.WriteFile(filepath.Join(os.Getenv("CAAM_HOME"), "config.yaml"), spmCfg, 0600); err != nil {
		t.Fatalf("WriteFile(config.yaml) error = %v", err)
	}

	// Use a temp vault.
	oldVault := vault
	vault = authfile.NewVault(filepath.Join(tmpDir, "vault"))
	t.Cleanup(func() { vault = oldVault })

	// Ensure _original exists so the first-activate backup does not suppress the smart backup.
	if err := os.MkdirAll(vault.ProfilePath("codex", "_original"), 0700); err != nil {
		t.Fatalf("MkdirAll(_original) error = %v", err)
	}

	// Pre-create an old auto-backup that should be rotated out.
	oldBackup := "_backup_20000101_000000"
	oldBackupDir := vault.ProfilePath("codex", oldBackup)
	if err := os.MkdirAll(oldBackupDir, 0700); err != nil {
		t.Fatalf("MkdirAll(old backup) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(oldBackupDir, "auth.json"), []byte(`{"access_token":"old"}`), 0600); err != nil {
		t.Fatalf("WriteFile(old backup auth) error = %v", err)
	}

	// Create a target profile in the vault with different contents.
	targetProfileDir := vault.ProfilePath("codex", "target")
	if err := os.MkdirAll(targetProfileDir, 0700); err != nil {
		t.Fatalf("MkdirAll(target profile) error = %v", err)
	}
	target := []byte(`{"access_token":"target","token_type":"Bearer"}`)
	if err := os.WriteFile(filepath.Join(targetProfileDir, "auth.json"), target, 0600); err != nil {
		t.Fatalf("WriteFile(target auth) error = %v", err)
	}

	if err := runActivate(activateCmd, []string{"codex", "target"}); err != nil {
		t.Fatalf("runActivate() error = %v", err)
	}

	// Ensure activation actually switched the auth file.
	gotActive, err := os.ReadFile(authPath)
	if err != nil {
		t.Fatalf("ReadFile(active auth) error = %v", err)
	}
	if string(gotActive) != string(target) {
		t.Fatalf("active auth mismatch: got %q want %q", gotActive, target)
	}

	// Ensure an auto-backup was created for the unsaved state and old backups were rotated out.
	entries, err := os.ReadDir(filepath.Join(vault.BasePath(), "codex"))
	if err != nil {
		t.Fatalf("ReadDir(codex) error = %v", err)
	}
	var backupProfiles []string
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), "_backup_") {
			backupProfiles = append(backupProfiles, e.Name())
		}
	}
	if len(backupProfiles) != 1 {
		t.Fatalf("found %d backup profiles, want 1: %v", len(backupProfiles), backupProfiles)
	}
	if backupProfiles[0] == oldBackup {
		t.Fatalf("expected old backup rotated out, but found %q", backupProfiles[0])
	}

	gotBackup, err := os.ReadFile(vault.BackupPath("codex", backupProfiles[0], "auth.json"))
	if err != nil {
		t.Fatalf("ReadFile(auto-backup auth) error = %v", err)
	}
	if string(gotBackup) != string(unsaved) {
		t.Fatalf("auto-backup auth mismatch: got %q want %q", gotBackup, unsaved)
	}
}

// TestActivate_RecapturesOutgoingBeforeOverwriting is the Switch safety
// rule of the switcher handoff (work item H): while a profile is active the
// tool rotates its refresh-token family in place, so the outgoing profile's
// vault copy must be refreshed from the live credential BEFORE the incoming
// one is installed — or the vault holds a consumed refresh token that a
// later switch back would replay.
func TestActivate_RecapturesOutgoingBeforeOverwriting(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("CODEX_HOME", filepath.Join(tmpDir, "codex_home"))
	t.Setenv("CAAM_HOME", filepath.Join(tmpDir, "caam_home"))
	if err := os.MkdirAll(os.Getenv("CODEX_HOME"), 0700); err != nil {
		t.Fatal(err)
	}
	oldVault := vault
	vault = authfile.NewVault(filepath.Join(tmpDir, "vault"))
	t.Cleanup(func() { vault = oldVault })

	base := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC).Unix()
	stale := testutil.SyntheticCodexAuth(t, "a@example.com", "a-stale", base)
	rotated := testutil.SyntheticCodexAuth(t, "a@example.com", "a-rotated", base+3600)
	incoming := testutil.SyntheticCodexAuth(t, "b@example.com", "b", base)

	write := func(path string, data []byte) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(vault.ProfilePath("codex", "a"), "auth.json"), stale)
	write(filepath.Join(vault.ProfilePath("codex", "b"), "auth.json"), incoming)
	livePath := filepath.Join(os.Getenv("CODEX_HOME"), "auth.json")
	write(livePath, rotated) // Codex rotated a's tokens since a was captured

	if err := runActivate(activateCmd, []string{"codex", "b"}); err != nil {
		t.Fatalf("runActivate: %v", err)
	}

	gotA, err := os.ReadFile(filepath.Join(vault.ProfilePath("codex", "a"), "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(gotA) != string(rotated) {
		t.Fatalf("outgoing profile a was not re-captured before the switch: vault holds %s", gotA)
	}
	gotLive, err := os.ReadFile(livePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotLive) != string(incoming) {
		t.Fatalf("live auth after switch = %s, want profile b", gotLive)
	}

	// Switching back installs the re-captured (rotated) tokens, not the
	// consumed ones that were in the vault before.
	if err := runActivate(activateCmd, []string{"codex", "a"}); err != nil {
		t.Fatalf("runActivate back to a: %v", err)
	}
	gotLive, _ = os.ReadFile(livePath)
	if string(gotLive) != string(rotated) {
		t.Fatalf("switching back replayed a stale credential: %s", gotLive)
	}
}

// TestActivate_AbortsWhenOutgoingCannotBeRecaptured: a switch that cannot
// refresh the outgoing vault copy must not overwrite the live credential,
// because that is exactly how the vault ends up holding a stale chain.
func TestActivate_AbortsWhenOutgoingCannotBeRecaptured(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	tmpDir := t.TempDir()
	t.Setenv("CODEX_HOME", filepath.Join(tmpDir, "codex_home"))
	t.Setenv("CAAM_HOME", filepath.Join(tmpDir, "caam_home"))
	if err := os.MkdirAll(os.Getenv("CODEX_HOME"), 0700); err != nil {
		t.Fatal(err)
	}
	oldVault := vault
	vault = authfile.NewVault(filepath.Join(tmpDir, "vault"))
	t.Cleanup(func() { vault = oldVault })

	base := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC).Unix()
	write := func(path string, data []byte) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	aDir := vault.ProfilePath("codex", "a")
	write(filepath.Join(aDir, "auth.json"), testutil.SyntheticCodexAuth(t, "a@example.com", "a", base))
	write(filepath.Join(vault.ProfilePath("codex", "b"), "auth.json"), testutil.SyntheticCodexAuth(t, "b@example.com", "b", base))
	livePath := filepath.Join(os.Getenv("CODEX_HOME"), "auth.json")
	rotated := testutil.SyntheticCodexAuth(t, "a@example.com", "a-rotated", base+3600)
	write(livePath, rotated)

	// The outgoing profile's vault directory cannot be written to.
	if err := os.Chmod(aDir, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(aDir, 0700) })

	err := runActivate(activateCmd, []string{"codex", "b"})
	if err == nil {
		t.Fatal("activate succeeded although the outgoing profile could not be re-captured")
	}
	if !strings.Contains(err.Error(), "re-capture") || !strings.Contains(err.Error(), "--force") {
		t.Errorf("error = %v, want the re-capture failure and the --force hint", err)
	}
	if got, _ := os.ReadFile(livePath); string(got) != string(rotated) {
		t.Fatal("live credential was overwritten although the switch was refused")
	}
}
