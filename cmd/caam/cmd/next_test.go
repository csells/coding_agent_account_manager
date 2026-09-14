package cmd

import (
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/testutil"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/config"
	caamdb "github.com/Dicklesworthstone/coding_agent_account_manager/internal/db"
	"github.com/spf13/cobra"
)

// setupNextTestEnv sets up a test environment with vault and profiles.
func setupNextTestEnv(t *testing.T) (tmpDir string, cleanup func()) {
	t.Helper()
	tmpDir = t.TempDir()

	oldCodexHome := os.Getenv("CODEX_HOME")
	oldCaamHome := os.Getenv("CAAM_HOME")

	_ = os.Setenv("CODEX_HOME", filepath.Join(tmpDir, "codex_home"))
	_ = os.Setenv("CAAM_HOME", filepath.Join(tmpDir, "caam_home"))

	if err := os.MkdirAll(os.Getenv("CODEX_HOME"), 0700); err != nil {
		t.Fatalf("MkdirAll(CODEX_HOME) error = %v", err)
	}
	if err := os.MkdirAll(os.Getenv("CAAM_HOME"), 0700); err != nil {
		t.Fatalf("MkdirAll(CAAM_HOME) error = %v", err)
	}

	// Use a temp vault
	oldVault := vault
	vault = authfile.NewVault(filepath.Join(tmpDir, "vault"))

	cleanup = func() {
		_ = os.Setenv("CODEX_HOME", oldCodexHome)
		_ = os.Setenv("CAAM_HOME", oldCaamHome)
		vault = oldVault
	}

	return tmpDir, cleanup
}

// createTestProfiles creates test profiles in the vault.
func createTestProfiles(t *testing.T, profiles map[string]string) {
	t.Helper()
	for name, token := range profiles {
		profPath := vault.ProfilePath("codex", name)
		if err := os.MkdirAll(profPath, 0700); err != nil {
			t.Fatalf("MkdirAll(profile %s) error = %v", name, err)
		}
		content := `{"access_token":"` + token + `"}`
		if err := os.WriteFile(filepath.Join(profPath, "auth.json"), []byte(content), 0600); err != nil {
			t.Fatalf("WriteFile(profile %s) error = %v", name, err)
		}
	}
}

func TestNext_RotatesToNextProfile(t *testing.T) {
	tmpDir, cleanup := setupNextTestEnv(t)
	defer cleanup()

	// Create current auth state (matches profile a)
	authPath := filepath.Join(os.Getenv("CODEX_HOME"), "auth.json")
	if err := os.WriteFile(authPath, []byte(`{"access_token":"a"}`), 0600); err != nil {
		t.Fatalf("WriteFile(current auth) error = %v", err)
	}

	// Create two profiles
	createTestProfiles(t, map[string]string{
		"a": "a",
		"b": "b",
	})

	// Enable rotation in SPM config
	spmCfg := []byte("version: 1\nstealth:\n  rotation:\n    enabled: true\n    algorithm: round_robin\n")
	if err := os.MkdirAll(filepath.Dir(config.SPMConfigPath()), 0700); err != nil {
		t.Fatalf("MkdirAll(config dir) error = %v", err)
	}
	if err := os.WriteFile(config.SPMConfigPath(), spmCfg, 0600); err != nil {
		t.Fatalf("WriteFile(config.yaml) error = %v", err)
	}
	_ = tmpDir // Suppress unused variable warning

	c := &cobra.Command{}
	c.Flags().Bool("dry-run", false, "")
	c.Flags().Bool("quiet", false, "")
	c.Flags().Bool("force", false, "")
	c.Flags().String("algorithm", "", "")

	if err := runNext(c, []string{"codex"}); err != nil {
		t.Fatalf("runNext() error = %v", err)
	}

	// Verify auth was switched to profile b
	got, err := os.ReadFile(authPath)
	if err != nil {
		t.Fatalf("ReadFile(active auth) error = %v", err)
	}
	if string(got) != `{"access_token":"b"}` {
		t.Fatalf("auth mismatch: got %q want %q", string(got), `{"access_token":"b"}`)
	}
}

func TestNext_SkipsCooldownProfile(t *testing.T) {
	_, cleanup := setupNextTestEnv(t)
	defer cleanup()

	// Create current auth state (matches profile a)
	authPath := filepath.Join(os.Getenv("CODEX_HOME"), "auth.json")
	if err := os.WriteFile(authPath, []byte(`{"access_token":"a"}`), 0600); err != nil {
		t.Fatalf("WriteFile(current auth) error = %v", err)
	}

	// Create three profiles
	createTestProfiles(t, map[string]string{
		"a": "a",
		"b": "b",
		"c": "c",
	})

	// Put profile b in cooldown
	db, err := caamdb.Open()
	if err != nil {
		t.Fatalf("db.Open() error = %v", err)
	}
	defer db.Close()

	if _, err := db.SetCooldown("codex", "b", time.Now().UTC(), 60*time.Minute, ""); err != nil {
		t.Fatalf("SetCooldown() error = %v", err)
	}

	c := &cobra.Command{}
	c.Flags().Bool("dry-run", false, "")
	c.Flags().Bool("quiet", false, "")
	c.Flags().Bool("force", false, "")
	c.Flags().String("algorithm", "smart", "")
	_ = c.Flags().Set("algorithm", "smart")

	if err := runNext(c, []string{"codex"}); err != nil {
		t.Fatalf("runNext() error = %v", err)
	}

	// Should have skipped b (cooldown) and picked c
	got, err := os.ReadFile(authPath)
	if err != nil {
		t.Fatalf("ReadFile(active auth) error = %v", err)
	}
	if string(got) != `{"access_token":"c"}` {
		t.Fatalf("auth mismatch: got %q want (c profile), not (b profile which is in cooldown)", string(got))
	}
}

func TestNext_DryRunDoesNotActivate(t *testing.T) {
	_, cleanup := setupNextTestEnv(t)
	defer cleanup()

	// Create current auth state
	authPath := filepath.Join(os.Getenv("CODEX_HOME"), "auth.json")
	originalContent := `{"access_token":"original"}`
	if err := os.WriteFile(authPath, []byte(originalContent), 0600); err != nil {
		t.Fatalf("WriteFile(current auth) error = %v", err)
	}

	// Create two profiles
	createTestProfiles(t, map[string]string{
		"a": "a",
		"b": "b",
	})

	c := &cobra.Command{}
	c.Flags().Bool("dry-run", true, "")
	c.Flags().Bool("quiet", false, "")
	c.Flags().Bool("force", false, "")
	c.Flags().String("algorithm", "", "")
	_ = c.Flags().Set("dry-run", "true")

	if err := runNext(c, []string{"codex"}); err != nil {
		t.Fatalf("runNext(--dry-run) error = %v", err)
	}

	// Verify auth was NOT changed
	got, err := os.ReadFile(authPath)
	if err != nil {
		t.Fatalf("ReadFile(active auth) error = %v", err)
	}
	if string(got) != originalContent {
		t.Fatalf("dry-run should not modify auth: got %q want %q", string(got), originalContent)
	}
}

func TestNext_SingleProfile_AlreadyActive(t *testing.T) {
	_, cleanup := setupNextTestEnv(t)
	defer cleanup()

	// Create current auth state that matches the only profile
	authPath := filepath.Join(os.Getenv("CODEX_HOME"), "auth.json")
	if err := os.WriteFile(authPath, []byte(`{"access_token":"only"}`), 0600); err != nil {
		t.Fatalf("WriteFile(current auth) error = %v", err)
	}

	// Create only one profile
	createTestProfiles(t, map[string]string{
		"only": "only",
	})

	c := &cobra.Command{}
	c.Flags().Bool("dry-run", false, "")
	c.Flags().Bool("quiet", false, "")
	c.Flags().Bool("force", false, "")
	c.Flags().String("algorithm", "", "")

	// Should not error, just inform that only one profile is available
	if err := runNext(c, []string{"codex"}); err != nil {
		t.Fatalf("runNext() error = %v", err)
	}
}

func TestNext_UnknownTool_ReturnsError(t *testing.T) {
	_, cleanup := setupNextTestEnv(t)
	defer cleanup()

	c := &cobra.Command{}
	c.Flags().Bool("dry-run", false, "")
	c.Flags().Bool("quiet", false, "")
	c.Flags().Bool("force", false, "")
	c.Flags().String("algorithm", "", "")

	err := runNext(c, []string{"unknown"})
	if err == nil {
		t.Fatal("runNext(unknown tool) should return error")
	}
	if !contains(err.Error(), "unknown tool") {
		t.Fatalf("error should mention 'unknown tool': %v", err)
	}
}

func TestNext_NoProfiles_ReturnsError(t *testing.T) {
	_, cleanup := setupNextTestEnv(t)
	defer cleanup()

	// Don't create any profiles

	c := &cobra.Command{}
	c.Flags().Bool("dry-run", false, "")
	c.Flags().Bool("quiet", false, "")
	c.Flags().Bool("force", false, "")
	c.Flags().String("algorithm", "", "")

	err := runNext(c, []string{"codex"})
	if err == nil {
		t.Fatal("runNext() with no profiles should return error")
	}
	if !contains(err.Error(), "no profiles found") {
		t.Fatalf("error should mention 'no profiles found': %v", err)
	}
}

func TestNext_AlgorithmOverride(t *testing.T) {
	_, cleanup := setupNextTestEnv(t)
	defer cleanup()

	// Create current auth state
	authPath := filepath.Join(os.Getenv("CODEX_HOME"), "auth.json")
	if err := os.WriteFile(authPath, []byte(`{"access_token":"a"}`), 0600); err != nil {
		t.Fatalf("WriteFile(current auth) error = %v", err)
	}

	// Create two profiles
	createTestProfiles(t, map[string]string{
		"a": "a",
		"b": "b",
	})

	c := &cobra.Command{}
	c.Flags().Bool("dry-run", false, "")
	c.Flags().Bool("quiet", false, "")
	c.Flags().Bool("force", false, "")
	c.Flags().String("algorithm", "round_robin", "")
	_ = c.Flags().Set("algorithm", "round_robin")

	if err := runNext(c, []string{"codex"}); err != nil {
		t.Fatalf("runNext(--algorithm round_robin) error = %v", err)
	}

	// Verify auth was switched
	got, err := os.ReadFile(authPath)
	if err != nil {
		t.Fatalf("ReadFile(active auth) error = %v", err)
	}
	if string(got) != `{"access_token":"b"}` {
		t.Fatalf("auth mismatch: got %q want %q", string(got), `{"access_token":"b"}`)
	}
}

// contains checks if substr is in s.
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// caam next switches through the shared core: when the outgoing profile
// cannot be re-captured the switch is refused with the live credential
// untouched, instead of a warning and a stale vault copy. --force proceeds.
func TestNext_AbortsWhenRecaptureFails(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	_, cleanup := setupNextTestEnv(t)
	defer cleanup()

	// Real-shaped credentials: the live file is a's, rotated since capture,
	// so it still reads as a but a re-capture has to write.
	base := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC).Unix()
	stale := testutil.SyntheticCodexAuth(t, "a@example.com", "a-stale", base)
	rotated := testutil.SyntheticCodexAuth(t, "a@example.com", "a-rotated", base+3600)
	incoming := testutil.SyntheticCodexAuth(t, "b@example.com", "b", base)
	write := func(path string, data []byte) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(vault.ProfilePath("codex", "a"), "auth.json"), stale)
	write(filepath.Join(vault.ProfilePath("codex", "b"), "auth.json"), incoming)
	authPath := filepath.Join(os.Getenv("CODEX_HOME"), "auth.json")
	write(authPath, rotated)
	spmCfg := []byte("version: 1\nstealth:\n  rotation:\n    enabled: true\n    algorithm: round_robin\n")
	if err := os.MkdirAll(filepath.Dir(config.SPMConfigPath()), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config.SPMConfigPath(), spmCfg, 0600); err != nil {
		t.Fatal(err)
	}
	// Make a's vault copy un-refreshable.
	aDir := vault.ProfilePath("codex", "a")
	if err := os.Chmod(aDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(aDir, 0o700) })

	newCmd := func(force bool) *cobra.Command {
		c := &cobra.Command{}
		c.Flags().Bool("dry-run", false, "")
		c.Flags().Bool("quiet", true, "")
		c.Flags().Bool("force", force, "")
		c.Flags().String("algorithm", "", "")
		return c
	}
	if err := runNext(newCmd(false), []string{"codex"}); err == nil {
		t.Fatal("runNext() should refuse when the outgoing profile cannot be re-captured")
	}
	if got, _ := os.ReadFile(authPath); string(got) != string(rotated) {
		t.Fatalf("live credential was replaced despite the refusal: %s", got)
	}
	if err := runNext(newCmd(true), []string{"codex"}); err != nil {
		t.Fatalf("forced runNext() error = %v", err)
	}
	if got, _ := os.ReadFile(authPath); string(got) != string(incoming) {
		t.Fatalf("forced next did not switch to b: %s", got)
	}
}
