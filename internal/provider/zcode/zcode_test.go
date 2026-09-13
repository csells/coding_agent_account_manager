package zcode

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/profile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/zcodecred"
)

// writeRecord seals a record under the process secret, as zcode would.
func writeRecord(t *testing.T, path string, values map[string]string) {
	t.Helper()
	sealed := map[string]string{}
	for k, v := range values {
		s, err := zcodecred.EncryptWith(v, zcodecred.Secret())
		if err != nil {
			t.Fatal(err)
		}
		sealed[k] = s
	}
	data, _ := json.Marshal(sealed)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestAuthFilesFollowDataBaseDir(t *testing.T) {
	p := New()
	t.Setenv(zcodecred.DataBaseDirEnv, "/z")
	files := p.AuthFiles()
	if len(files) != 1 || files[0].Path != filepath.Join("/z", ".zcode", "v2", "credentials.json") || !files[0].Required {
		t.Fatalf("AuthFiles = %+v", files)
	}
}

func TestDetectExistingAuthUnsealsToConfirmLogin(t *testing.T) {
	base := t.TempDir()
	t.Setenv(zcodecred.DataBaseDirEnv, base)
	t.Setenv(zcodecred.SecretEnv, "unit-secret")
	p := New()

	if det, _ := p.DetectExistingAuth(); det.Found {
		t.Fatal("found a login with no file")
	}
	path := zcodecred.DefaultPath()
	writeRecord(t, path, map[string]string{zcodecred.KeyActiveProvider: "zai"})
	if det, _ := p.DetectExistingAuth(); det.Found {
		t.Fatalf("a record without a session counted as a login: %+v", det.Locations[0])
	}
	writeRecord(t, path, map[string]string{
		zcodecred.KeyActiveProvider: "zai",
		zcodecred.KeyJWTToken:       "SYNTHETIC-JWT",
		zcodecred.KeyUserInfo:       `{"email":"dev@example.com","name":"Dev","user_id":"u-1"}`,
	})
	det, _ := p.DetectExistingAuth()
	if !det.Found || det.Primary == nil || det.Primary.Path != path {
		t.Fatalf("logged-in record: %+v", det)
	}

	// A record sealed under another secret is present but unreadable, and
	// says so rather than reporting a login.
	t.Setenv(zcodecred.SecretEnv, "other-secret")
	det, _ = p.DetectExistingAuth()
	if det.Found || det.Locations[0].ValidationError == "" {
		t.Fatalf("foreign record: %+v", det.Locations[0])
	}
}

func TestProfileLifecycle(t *testing.T) {
	t.Setenv(zcodecred.SecretEnv, "unit-secret")
	store := profile.NewStore(t.TempDir())
	prof, err := store.Create("zcode", "work", "oauth")
	if err != nil {
		t.Fatal(err)
	}
	p := New()
	ctx := context.Background()
	if err := p.PrepareProfile(ctx, prof); err != nil {
		t.Fatalf("PrepareProfile: %v", err)
	}
	env, _ := p.Env(ctx, prof)
	if env["HOME"] != prof.HomePath() || env[zcodecred.DataBaseDirEnv] != prof.HomePath() {
		t.Fatalf("Env = %v", env)
	}
	if st, _ := p.Status(ctx, prof); st.LoggedIn {
		t.Fatal("fresh profile reports logged in")
	}

	src := filepath.Join(t.TempDir(), "credentials.json")
	writeRecord(t, src, map[string]string{
		zcodecred.KeyJWTToken: "SYNTHETIC-JWT",
		zcodecred.KeyUserInfo: `{"email":"dev@example.com","name":"Dev","user_id":"u-1"}`,
	})
	if _, err := p.ImportAuth(ctx, src, prof); err != nil {
		t.Fatalf("ImportAuth: %v", err)
	}
	st, _ := p.Status(ctx, prof)
	if !st.LoggedIn || st.AccountID != "dev@example.com" {
		t.Fatalf("Status after import = %+v", st)
	}
	if res, _ := p.ValidateToken(ctx, prof, true); !res.Valid {
		t.Fatalf("ValidateToken = %+v", res)
	}
	if err := p.Logout(ctx, prof); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if st, _ := p.Status(ctx, prof); st.LoggedIn {
		t.Fatal("still logged in after Logout")
	}
}
