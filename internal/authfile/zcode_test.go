package authfile

// zcode adapter (switcher handoff, work item F). Every fixture here is
// SYNTHETIC; no test reads the real ~/.zcode.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/zcodecred"
)

// zcodeRecord seals a record under the process secret, as zcode would.
func zcodeRecord(t *testing.T, path string, values map[string]string) {
	t.Helper()
	sealed := map[string]string{}
	for k, val := range values {
		s, err := zcodecred.EncryptWith(val, zcodecred.Secret())
		if err != nil {
			t.Fatal(err)
		}
		sealed[k] = s
	}
	data, _ := json.MarshalIndent(sealed, "", "  ")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestZcodeAuthFiles_Shape(t *testing.T) {
	t.Setenv(zcodecred.DataBaseDirEnv, "/z")
	fs := ZcodeAuthFiles()
	if fs.Tool != "zcode" || len(fs.Files) != 1 || !fs.Files[0].Required {
		t.Fatalf("ZcodeAuthFiles = %+v", fs)
	}
	if fs.Files[0].Path != filepath.Join("/z", ".zcode", "v2", "credentials.json") {
		t.Errorf("path = %q", fs.Files[0].Path)
	}
}

func TestZcode_BackupRestoreAndResealTolerantDetection(t *testing.T) {
	base := t.TempDir()
	t.Setenv(zcodecred.DataBaseDirEnv, base)
	t.Setenv(zcodecred.SecretEnv, "unit-secret")
	fs := ZcodeAuthFiles()
	path := fs.Files[0].Path
	v := NewVault(t.TempDir())

	// A record without a session is a logout, not a login.
	zcodeRecord(t, path, map[string]string{zcodecred.KeyActiveProvider: "zai"})
	if HasAuthFiles(fs) {
		t.Fatal("HasAuthFiles reported a session-less record as a login")
	}
	if err := v.Backup(fs, "work"); err == nil || !strings.Contains(err.Error(), "logged out") {
		t.Fatalf("Backup of a session-less record = %v, want a logged-out error", err)
	}

	work := map[string]string{
		zcodecred.KeyActiveProvider: "zai",
		zcodecred.KeyJWTToken:       "SYNTHETIC-SESSION-WORK",
		zcodecred.KeyUserInfo:       `{"email":"work@example.com","name":"Work","user_id":"u-work"}`,
	}
	zcodeRecord(t, path, work)
	if !HasAuthFiles(fs) {
		t.Fatal("HasAuthFiles missed a zcode login")
	}
	if err := v.Backup(fs, "work"); err != nil {
		t.Fatalf("Backup(work): %v", err)
	}
	if id := v.ProfileIdentity("zcode", "work"); id != "work@example.com" {
		t.Fatalf("ProfileIdentity = %q, want the sealed profile's email", id)
	}
	zcodeRecord(t, path, map[string]string{
		zcodecred.KeyActiveProvider: "zai",
		zcodecred.KeyJWTToken:       "SYNTHETIC-SESSION-PERSONAL",
		zcodecred.KeyUserInfo:       `{"email":"me@example.com","name":"Me","user_id":"u-me"}`,
	})
	if err := v.Backup(fs, "personal"); err != nil {
		t.Fatalf("Backup(personal): %v", err)
	}
	if err := v.Restore(fs, "work"); err != nil {
		t.Fatalf("Restore(work): %v", err)
	}
	rec, err := zcodecred.ReadRecord(path)
	if err != nil || rec.JWTToken != "SYNTHETIC-SESSION-WORK" {
		t.Fatalf("restored record = %+v, %v", rec, err)
	}
	if active, _ := v.ActiveProfile(fs); active != "work" {
		t.Fatalf("ActiveProfile = %q, want work", active)
	}
	// zcode re-seals the same values with a fresh IV whenever it rewrites
	// the file: different bytes, same account.
	zcodeRecord(t, path, work)
	if active, _ := v.ActiveProfile(fs); active != "work" {
		t.Fatalf("ActiveProfile after re-seal = %q, want work", active)
	}
}
