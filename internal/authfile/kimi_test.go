package authfile

// Kimi Code adapter (switcher handoff, work item E). Every fixture here is
// SYNTHETIC; no test reads the real ~/.kimi-code.

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func kimiToken(t *testing.T, sub, access string) string {
	t.Helper()
	claims, _ := json.Marshal(map[string]any{"sub": sub, "exp": 1893456000})
	jwt := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`)) + "." + base64.RawURLEncoding.EncodeToString(claims) + ".sig"
	return `{"access_token":"` + jwt + `","refresh_token":"SYNTHETIC-RT-` + access + `","expires_at":1893456000,"scope":"kimi-code","token_type":"Bearer","expires_in":3600}`
}

const kimiLoggedOut = `{"access_token":"","refresh_token":"","expires_at":0,"scope":"kimi-code","token_type":"Bearer","expires_in":0}`

func TestKimiAuthFiles_Shape(t *testing.T) {
	t.Setenv("KIMI_CODE_HOME", "/k")
	fs := KimiAuthFiles()
	if fs.Tool != "kimi" || len(fs.Files) != 1 || !fs.Files[0].Required || fs.AllowOptionalOnly {
		t.Fatalf("KimiAuthFiles = %+v", fs)
	}
	if fs.Files[0].Path != filepath.Join("/k", "credentials", "kimi-code.json") {
		t.Errorf("path = %q", fs.Files[0].Path)
	}
	if got, ok := GetAuthFileSet("kimi-code"); !ok || got.Tool != "kimi" {
		t.Errorf("GetAuthFileSet(kimi-code) = %+v, %v", got, ok)
	}
}

func TestKimi_LoggedOutFileIsNotALogin(t *testing.T) {
	home := t.TempDir()
	t.Setenv("KIMI_CODE_HOME", home)
	fs := KimiAuthFiles()
	path := fs.Files[0].Path
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(kimiLoggedOut), 0600); err != nil {
		t.Fatal(err)
	}
	if HasAuthFiles(fs) {
		t.Fatal("HasAuthFiles reported the CLI's logged-out file as a login")
	}
	v := NewVault(t.TempDir())
	err := v.Backup(fs, "work")
	if err == nil {
		t.Fatal("Backup captured a logged-out file")
	}
	if !strings.Contains(err.Error(), "logged out") {
		t.Errorf("Backup error does not say why: %v", err)
	}
}

func TestKimi_BackupRestoreAndRotationTolerantDetection(t *testing.T) {
	home := t.TempDir()
	t.Setenv("KIMI_CODE_HOME", home)
	fs := KimiAuthFiles()
	path := fs.Files[0].Path
	write := func(content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	v := NewVault(t.TempDir())

	write(kimiToken(t, "user-work", "1"))
	if !HasAuthFiles(fs) {
		t.Fatal("HasAuthFiles missed a Kimi login")
	}
	if err := v.Backup(fs, "work"); err != nil {
		t.Fatalf("Backup(work): %v", err)
	}
	write(kimiToken(t, "user-personal", "2"))
	if err := v.Backup(fs, "personal"); err != nil {
		t.Fatalf("Backup(personal): %v", err)
	}
	if err := v.Restore(fs, "work"); err != nil {
		t.Fatalf("Restore(work): %v", err)
	}
	if got, _ := os.ReadFile(path); string(got) != kimiToken(t, "user-work", "1") {
		t.Fatal("Restore did not put the work token in place")
	}
	if active, _ := v.ActiveProfile(fs); active != "work" {
		t.Fatalf("ActiveProfile = %q, want work", active)
	}
	// The CLI rotates both tokens for the same account.
	write(kimiToken(t, "user-work", "rotated"))
	if active, _ := v.ActiveProfile(fs); active != "work" {
		t.Fatalf("ActiveProfile after rotation = %q, want work", active)
	}
	if err := v.RecordProfileIdentity("kimi", "work", "work@example.com"); err != nil {
		t.Fatal(err)
	}
	if id := v.ProfileIdentity("kimi", "work"); id != "work@example.com" {
		t.Fatalf("ProfileIdentity = %q", id)
	}
}
