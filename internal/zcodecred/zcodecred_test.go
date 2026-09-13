package zcodecred

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testSecret = "unit-test-secret"

// WriteFixture seals a record the way zcode does and writes it to path.
func writeFixture(t *testing.T, path string, values map[string]string) {
	t.Helper()
	sealed := map[string]string{}
	for k, v := range values {
		s, err := EncryptWith(v, testSecret)
		if err != nil {
			t.Fatal(err)
		}
		sealed[k] = s
	}
	data, err := json.MarshalIndent(sealed, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSealRoundTrip(t *testing.T) {
	sealed, err := EncryptWith("zai", testSecret)
	if err != nil {
		t.Fatal(err)
	}
	if !IsSealed(sealed) || strings.Count(strings.TrimPrefix(sealed, Prefix), ".") != 2 {
		t.Fatalf("sealed value has the wrong shape: %q", sealed)
	}
	plain, err := DecryptWith(sealed, testSecret)
	if err != nil || plain != "zai" {
		t.Fatalf("DecryptWith = %q, %v", plain, err)
	}
	if _, err := DecryptWith(sealed, "other-secret"); err == nil {
		t.Fatal("a wrong secret must not decrypt")
	}
	if plain, err := DecryptWith("plain", testSecret); err != nil || plain != "plain" {
		t.Fatalf("a plain value must pass through: %q, %v", plain, err)
	}
	for _, bad := range []string{Prefix + "a.b", Prefix + "..", Prefix + "!!.a.b"} {
		if _, err := DecryptWith(bad, testSecret); err == nil {
			t.Errorf("malformed %q decrypted", bad)
		}
	}
}

func TestSecretFallbackIsPerUser(t *testing.T) {
	t.Setenv(SecretEnv, "")
	s := Secret()
	home, _ := os.UserHomeDir()
	if !strings.HasPrefix(s, "zcode-credential-fallback:") || !strings.Contains(s, home) {
		t.Fatalf("fallback secret = %q", s)
	}
	t.Setenv(SecretEnv, " explicit ")
	if Secret() != "explicit" {
		t.Fatalf("explicit secret = %q", Secret())
	}
}

func TestReadRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".zcode", "v2", "credentials.json")
	if _, err := ReadRecordWith(path, testSecret); !errors.Is(err, ErrNoRecord) {
		t.Fatalf("missing file = %v, want ErrNoRecord", err)
	}
	writeFixture(t, path, map[string]string{
		KeyActiveProvider: "zai",
		KeyAccessToken:    "SYNTHETIC-ACCESS",
		KeyJWTToken:       "SYNTHETIC-JWT",
		KeyUserInfo:       `{"email":"dev@example.com","name":"Dev","user_id":"u-1","avatar":""}`,
	})
	rec, err := ReadRecordWith(path, testSecret)
	if err != nil {
		t.Fatalf("ReadRecordWith: %v", err)
	}
	if rec.ActiveProvider != "zai" || rec.AccessToken != "SYNTHETIC-ACCESS" || rec.JWTToken != "SYNTHETIC-JWT" || rec.RefreshToken != "" {
		t.Errorf("record = %+v", rec)
	}
	if rec.UserInfo == nil || rec.UserInfo.Email != "dev@example.com" || rec.UserInfo.UserID != "u-1" {
		t.Errorf("user info = %+v", rec.UserInfo)
	}
	if !rec.LoggedIn() {
		t.Error("record with tokens must count as logged in")
	}
	if _, err := ReadRecordWith(path, "wrong"); err == nil {
		t.Fatal("a wrong secret must surface as an error, not an empty record")
	}

	// DefaultPath follows ZCODE_DATA_BASE_DIR.
	t.Setenv(DataBaseDirEnv, "/base")
	if got := DefaultPath(); got != filepath.Join("/base", ".zcode", "v2", "credentials.json") {
		t.Errorf("DefaultPath = %q", got)
	}
}
