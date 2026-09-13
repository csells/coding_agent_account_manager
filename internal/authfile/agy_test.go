package authfile

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// NOTE: every fixture in this file is SYNTHETIC. No test reads, writes, or
// copies the real ~/.gemini credentials. "Tokens" here are non-secret literals.

const (
	agyFakeToken    = `{"auth_method":"oauth","token":"SYNTHETIC-AGY-TOKEN-NOT-REAL"}`
	agyFakeAccounts = `{"active":"work@example.com","old":["old@example.com"]}`
	agyFakeCreds    = `{"access_token":"synthetic","refresh_token":"synthetic"}`
)

func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// agyFixtureHome lays down a synthetic agy auth tree under GEMINI_HOME and
// returns the file set + the token/accounts/creds paths.
func agyFixtureHome(t *testing.T) (AuthFileSet, string, string, string) {
	t.Helper()
	home := t.TempDir()
	gemHome := filepath.Join(home, ".gemini")
	t.Setenv("GEMINI_HOME", gemHome)

	antigravityDir := filepath.Join(gemHome, "antigravity-cli")
	if err := os.MkdirAll(antigravityDir, 0700); err != nil {
		t.Fatal(err)
	}

	tokenPath := filepath.Join(antigravityDir, "antigravity-oauth-token")
	accountsPath := filepath.Join(gemHome, "google_accounts.json")
	credsPath := filepath.Join(gemHome, "oauth_creds.json")

	if err := os.WriteFile(tokenPath, []byte(agyFakeToken), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(accountsPath, []byte(agyFakeAccounts), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(credsPath, []byte(agyFakeCreds), 0600); err != nil {
		t.Fatal(err)
	}

	return AntigravityAuthFiles(), tokenPath, accountsPath, credsPath
}

func TestAntigravityAuthFiles_Shape(t *testing.T) {
	t.Setenv("GEMINI_HOME", "/g/.gemini")
	fs := AntigravityAuthFiles()
	if fs.Tool != "agy" {
		t.Errorf("Tool = %q, want agy", fs.Tool)
	}
	if len(fs.Files) == 0 || !fs.Files[0].Required {
		t.Fatal("first file (token) must be required")
	}
	if filepath.Base(fs.Files[0].Path) != "antigravity-oauth-token" {
		t.Errorf("first file = %q, want antigravity-oauth-token", filepath.Base(fs.Files[0].Path))
	}
	// No basename collisions in the vault.
	seen := map[string]bool{}
	for _, f := range fs.Files {
		b := filepath.Base(f.Path)
		if seen[b] {
			t.Errorf("duplicate vault basename %q", b)
		}
		seen[b] = true
	}
}

func TestGetAuthFileSet_AgyAlias(t *testing.T) {
	for _, name := range []string{"agy", "antigravity", "AGY"} {
		fs, ok := GetAuthFileSet(name)
		if !ok {
			t.Errorf("GetAuthFileSet(%q) not found", name)
			continue
		}
		if fs.Tool != "agy" {
			t.Errorf("GetAuthFileSet(%q).Tool = %q, want agy", name, fs.Tool)
		}
	}
}

// TestAgy_BackupRestoreRoundTrip is the core 14.2 round-trip: backup a synthetic
// agy auth tree to a vault, clear it, restore it, and assert every file is
// byte-identical. It compares SHA-256 hashes only; it never prints contents.
func TestAgy_BackupRestoreRoundTrip(t *testing.T) {
	fs, tokenPath, accountsPath, credsPath := agyFixtureHome(t)
	vaultDir := t.TempDir()
	v := NewVault(vaultDir)

	// Capture original hashes (no content printed).
	origHashes := map[string]string{}
	for _, p := range []string{tokenPath, accountsPath, credsPath} {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("read %s: %v", p, err)
		}
		origHashes[filepath.Base(p)] = sha256Hex(data)
	}

	// Backup.
	if err := v.Backup(fs, "work"); err != nil {
		t.Fatalf("Backup() error = %v", err)
	}

	// The vault should contain the token (required) and optional files.
	if _, err := os.Stat(v.BackupPath("agy", "work", "antigravity-oauth-token")); err != nil {
		t.Fatalf("token not backed up: %v", err)
	}

	// Clear current auth (simulate logout / account switch away).
	if err := ClearAuthFiles(fs); err != nil {
		t.Fatalf("ClearAuthFiles() error = %v", err)
	}
	if HasAuthFiles(fs) {
		t.Fatal("HasAuthFiles should be false after clear")
	}

	// Restore.
	if err := v.Restore(fs, "work"); err != nil {
		t.Fatalf("Restore() error = %v", err)
	}

	// Every restored file must be byte-identical (hash match).
	for _, p := range []string{tokenPath, accountsPath, credsPath} {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("restored read %s: %v", p, err)
		}
		if got := sha256Hex(data); got != origHashes[filepath.Base(p)] {
			t.Errorf("restored %s is NOT byte-identical (hash mismatch)", filepath.Base(p))
		}
		// Restored files must be 0600.
		info, _ := os.Stat(p)
		if info.Mode().Perm() != 0600 {
			t.Errorf("restored %s perms = %o, want 0600", filepath.Base(p), info.Mode().Perm())
		}
	}

	if !HasAuthFiles(fs) {
		t.Error("HasAuthFiles should be true after restore")
	}
}

// TestAgy_BackupRequiresToken proves the token file is genuinely required: a
// backup with only the optional files present must fail.
func TestAgy_BackupRequiresToken(t *testing.T) {
	home := t.TempDir()
	gemHome := filepath.Join(home, ".gemini")
	t.Setenv("GEMINI_HOME", gemHome)
	if err := os.MkdirAll(gemHome, 0700); err != nil {
		t.Fatal(err)
	}
	// Only the optional accounts file exists; no token.
	if err := os.WriteFile(filepath.Join(gemHome, "google_accounts.json"), []byte(agyFakeAccounts), 0600); err != nil {
		t.Fatal(err)
	}

	v := NewVault(t.TempDir())
	if err := v.Backup(AntigravityAuthFiles(), "broken"); err == nil {
		t.Error("Backup() should fail without the required token file")
	}
}

// TestAgy_ActiveProfile verifies account switching detection: after backing up
// two distinct accounts, restoring one makes ActiveProfile report that profile.
func TestAgy_ActiveProfile(t *testing.T) {
	fs, tokenPath, accountsPath, _ := agyFixtureHome(t)
	v := NewVault(t.TempDir())

	// Profile "work" = the current fixture.
	if err := v.Backup(fs, "work"); err != nil {
		t.Fatalf("Backup(work) error = %v", err)
	}

	// Mutate to a different account ("personal") and back that up too.
	personalToken := `{"auth_method":"oauth","token":"SYNTHETIC-PERSONAL-TOKEN"}`
	personalAccounts := `{"active":"me@personal.example","old":[]}`
	if err := os.WriteFile(tokenPath, []byte(personalToken), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(accountsPath, []byte(personalAccounts), 0600); err != nil {
		t.Fatal(err)
	}
	if err := v.Backup(fs, "personal"); err != nil {
		t.Fatalf("Backup(personal) error = %v", err)
	}

	// Switch back to "work" and confirm detection.
	if err := v.Restore(fs, "work"); err != nil {
		t.Fatalf("Restore(work) error = %v", err)
	}
	active, err := v.ActiveProfile(fs)
	if err != nil {
		t.Fatalf("ActiveProfile() error = %v", err)
	}
	if active != "work" {
		t.Errorf("ActiveProfile() = %q, want work", active)
	}

	// Switch to "personal" and confirm.
	if err := v.Restore(fs, "personal"); err != nil {
		t.Fatalf("Restore(personal) error = %v", err)
	}
	active, _ = v.ActiveProfile(fs)
	if active != "personal" {
		t.Errorf("ActiveProfile() = %q, want personal", active)
	}
}

// TestAgy_ProfileIdentity verifies account-identification parsing from a vault
// profile (the active Google email from google_accounts.json).
func TestAgy_ProfileIdentity(t *testing.T) {
	fs, _, _, _ := agyFixtureHome(t)
	v := NewVault(t.TempDir())
	if err := v.Backup(fs, "work"); err != nil {
		t.Fatalf("Backup() error = %v", err)
	}
	if got := v.ProfileIdentity("agy", "work"); got != "work@example.com" {
		t.Errorf("ProfileIdentity() = %q, want work@example.com", got)
	}
}

var _ = agyFakeCreds

// TestAgy_ActiveProfileSurvivesTokenRotation: Google renews the access token
// every hour and agy rewrites the token file (or keychain item) in place, so
// the byte hash stops matching the profile's own snapshot within the hour.
// The active Google account beside it still identifies the profile.
func TestAgy_ActiveProfileSurvivesTokenRotation(t *testing.T) {
	fs, tokenPath, accountsPath, _ := agyFixtureHome(t)
	v := NewVault(t.TempDir())
	if err := v.Backup(fs, "work"); err != nil {
		t.Fatalf("Backup(work): %v", err)
	}
	// A second account in the vault, so the fallback has to discriminate.
	if err := os.WriteFile(accountsPath, []byte(`{"active":"me@personal.example","old":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tokenPath, []byte(`{"auth_method":"oauth","token":"SYNTHETIC-PERSONAL"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := v.Backup(fs, "personal"); err != nil {
		t.Fatalf("Backup(personal): %v", err)
	}
	if err := v.Restore(fs, "work"); err != nil {
		t.Fatalf("Restore(work): %v", err)
	}

	// agy rotates the token in place; google_accounts.json is untouched.
	if err := os.WriteFile(tokenPath, []byte(`{"auth_method":"oauth","token":"SYNTHETIC-ROTATED-WORK"}`), 0600); err != nil {
		t.Fatal(err)
	}
	active, err := v.ActiveProfile(fs)
	if err != nil {
		t.Fatalf("ActiveProfile: %v", err)
	}
	if active != "work" {
		t.Fatalf("ActiveProfile after rotation = %q, want work", active)
	}

	// With no google_accounts.json at all there is nothing to fall back on.
	if err := os.Remove(accountsPath); err != nil {
		t.Fatal(err)
	}
	if active, _ := v.ActiveProfile(fs); active != "" {
		t.Fatalf("ActiveProfile without an identity file = %q, want none", active)
	}
}

// TestAgy_StableHashFollowsTheRefreshToken: the hash that ActiveProfile
// compares must ignore the hourly access-token rotation and change only
// with the refresh token, i.e. with a new login.
func TestAgy_StableHashFollowsTheRefreshToken(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	a := write("a/antigravity-oauth-token", `{"auth_method":"oauth","token":{"access_token":"SYNTHETIC-1","refresh_token":"SYNTHETIC-RT","expiry":"2030-01-01T00:00:00Z"}}`)
	b := write("b/antigravity-oauth-token", `{"auth_method":"oauth","token":{"access_token":"SYNTHETIC-2","refresh_token":"SYNTHETIC-RT","expiry":"2030-01-01T01:00:00Z"}}`)
	c := write("c/antigravity-oauth-token", `{"auth_method":"oauth","token":{"access_token":"SYNTHETIC-2","refresh_token":"OTHER-RT","expiry":"2030-01-01T01:00:00Z"}}`)
	ha, _ := stableFileHash("agy", a)
	hb, _ := stableFileHash("agy", b)
	hc, _ := stableFileHash("agy", c)
	if ha != hb {
		t.Error("a rotated access token changed the stable hash")
	}
	if ha == hc {
		t.Error("a different refresh token did not change the stable hash")
	}
	// A legacy bare-token file still hashes whole.
	d := write("d/antigravity-oauth-token", `{"auth_method":"oauth","token":"SYNTHETIC-BARE"}`)
	if hd, err := stableFileHash("agy", d); err != nil || hd == ha {
		t.Errorf("bare token hash = %q, %v", hd, err)
	}
}

// TestAgy_RecordProfileIdentity: the Antigravity token carries no identity
// and google_accounts.json may name no active account, so the email caam
// resolves from Google at backup time is recorded in meta.json and read back
// as the profile's identity.
func TestAgy_RecordProfileIdentity(t *testing.T) {
	fs, _, accountsPath, _ := agyFixtureHome(t)
	if err := os.WriteFile(accountsPath, []byte(`{"active":null,"old":["someone@example.com"]}`), 0600); err != nil {
		t.Fatal(err)
	}
	v := NewVault(t.TempDir())
	if err := v.Backup(fs, "chris"); err != nil {
		t.Fatalf("Backup: %v", err)
	}
	if id := v.ProfileIdentity("agy", "chris"); id != "" {
		t.Fatalf("identity before recording = %q, want none", id)
	}
	if err := v.RecordProfileIdentity("agy", "chris", " chris@example.com "); err != nil {
		t.Fatalf("RecordProfileIdentity: %v", err)
	}
	if id := v.ProfileIdentity("agy", "chris"); id != "chris@example.com" {
		t.Fatalf("identity after recording = %q", id)
	}
	// The rest of meta.json survives the rewrite.
	raw, err := os.ReadFile(filepath.Join(v.ProfilePath("agy", "chris"), "meta.json"))
	if err != nil {
		t.Fatal(err)
	}
	var meta struct {
		Tool          string   `json:"tool"`
		Identity      string   `json:"identity"`
		OriginalPaths []string `json:"original_paths"`
	}
	if err := json.Unmarshal(raw, &meta); err != nil {
		t.Fatalf("meta.json unparseable after rewrite: %v", err)
	}
	if meta.Tool != "agy" || meta.Identity != "chris@example.com" || len(meta.OriginalPaths) == 0 {
		t.Errorf("meta.json after rewrite = %+v", meta)
	}
	if err := v.RecordProfileIdentity("agy", "nobody", "x@example.com"); err == nil {
		t.Error("recording on a profile that is not in the vault must fail")
	}
	if err := v.RecordProfileIdentity("agy", "chris", "  "); err == nil {
		t.Error("recording an empty identity must fail")
	}
}
