package kimi

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/profile"
)

const loggedIn = `{"access_token":"SYNTHETIC-AT","refresh_token":"SYNTHETIC-RT","expires_at":1893456000,"scope":"kimi-code","token_type":"Bearer","expires_in":3600}`
const loggedOut = `{"access_token":"","refresh_token":"","expires_at":0,"scope":"kimi-code","token_type":"Bearer","expires_in":0}`

func TestAuthFilesFollowKimiHome(t *testing.T) {
	p := New()
	if p.ID() != "kimi" || p.DefaultBin() != "kimi" {
		t.Fatalf("id/bin = %q/%q", p.ID(), p.DefaultBin())
	}
	t.Setenv(HomeEnv, "/k")
	files := p.AuthFiles()
	if len(files) != 1 || files[0].Path != filepath.Join("/k", "credentials", "kimi-code.json") || !files[0].Required {
		t.Fatalf("AuthFiles = %+v", files)
	}
	t.Setenv(HomeEnv, "")
	home, _ := os.UserHomeDir()
	if got := p.AuthFiles()[0].Path; got != filepath.Join(home, ".kimi-code", "credentials", "kimi-code.json") {
		t.Fatalf("default path = %q", got)
	}
}

func TestDetectExistingAuthTellsLoggedOutFromLoggedIn(t *testing.T) {
	home := t.TempDir()
	t.Setenv(HomeEnv, home)
	p := New()

	det, err := p.DetectExistingAuth()
	if err != nil || det.Found {
		t.Fatalf("no file: found=%v err=%v", det.Found, err)
	}

	path := CredentialsPath(home)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	// The CLI leaves the file behind with empty tokens after /logout; that
	// is not a login and must not be imported as one.
	if err := os.WriteFile(path, []byte(loggedOut), 0600); err != nil {
		t.Fatal(err)
	}
	det, _ = p.DetectExistingAuth()
	if det.Found || !strings.Contains(det.Locations[0].ValidationError, "logged out") {
		t.Fatalf("logged-out file: %+v", det.Locations[0])
	}

	if err := os.WriteFile(path, []byte(loggedIn), 0600); err != nil {
		t.Fatal(err)
	}
	det, _ = p.DetectExistingAuth()
	if !det.Found || det.Primary == nil || det.Primary.Path != path {
		t.Fatalf("logged-in file: %+v", det)
	}
}

func TestProfileLifecycle(t *testing.T) {
	store := profile.NewStore(t.TempDir())
	prof, err := store.Create("kimi", "work", "oauth")
	if err != nil {
		t.Fatal(err)
	}
	p := New()
	ctx := context.Background()
	if err := p.PrepareProfile(ctx, prof); err != nil {
		t.Fatalf("PrepareProfile: %v", err)
	}
	env, _ := p.Env(ctx, prof)
	if env["HOME"] != prof.HomePath() || env[HomeEnv] != filepath.Join(prof.HomePath(), ".kimi-code") {
		t.Fatalf("Env = %v", env)
	}

	st, _ := p.Status(ctx, prof)
	if st.LoggedIn {
		t.Fatal("fresh profile reports logged in")
	}

	src := filepath.Join(t.TempDir(), "kimi-code.json")
	if err := os.WriteFile(src, []byte(loggedIn), 0600); err != nil {
		t.Fatal(err)
	}
	copied, err := p.ImportAuth(ctx, src, prof)
	if err != nil {
		t.Fatalf("ImportAuth: %v", err)
	}
	if len(copied) != 1 || copied[0] != CredentialsPath(filepath.Join(prof.HomePath(), ".kimi-code")) {
		t.Fatalf("ImportAuth copied %v", copied)
	}
	st, _ = p.Status(ctx, prof)
	if !st.LoggedIn || st.ExpiresAt == "" {
		t.Fatalf("Status after import = %+v", st)
	}
	res, _ := p.ValidateToken(ctx, prof, true)
	if !res.Valid || res.ExpiresAt.IsZero() {
		t.Fatalf("ValidateToken = %+v", res)
	}
	if err := p.Logout(ctx, prof); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if st, _ := p.Status(ctx, prof); st.LoggedIn {
		t.Fatal("still logged in after Logout")
	}
	if res, _ := p.ValidateToken(ctx, prof, true); res.Valid {
		t.Fatal("ValidateToken valid after Logout")
	}
}
