package identity

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/zcodecred"
)

func TestExtractFromZcodeCredentials(t *testing.T) {
	t.Setenv(zcodecred.SecretEnv, "unit-secret")
	path := filepath.Join(t.TempDir(), "credentials.json")
	seal := func(v string) string {
		t.Helper()
		s, err := zcodecred.EncryptWith(v, "unit-secret")
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	access := syntheticJWT(t, map[string]any{"sub": "sub-1", "exp": 1893456000, "iat": 1893452400})
	rec := map[string]string{
		zcodecred.KeyActiveProvider: seal("zai"),
		zcodecred.KeyAccessToken:    seal(access),
		zcodecred.KeyJWTToken:       seal("SYNTHETIC-SESSION"),
		zcodecred.KeyUserInfo:       seal(`{"email":"dev@example.com","name":"Dev","user_id":"u-9","avatar":""}`),
	}
	data, _ := json.Marshal(rec)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	id, err := ExtractFromZcodeCredentials(path)
	if err != nil {
		t.Fatalf("ExtractFromZcodeCredentials: %v", err)
	}
	if id.Provider != "zcode" || id.Email != "dev@example.com" || id.AccountID != "u-9" || id.Organization != "Dev" {
		t.Errorf("identity = %+v", id)
	}
	if id.ExpiresAt.UTC().Year() != 2030 {
		t.Errorf("ExpiresAt = %v, want the access token's exp", id.ExpiresAt)
	}

	t.Setenv(zcodecred.SecretEnv, "someone-else")
	if _, err := ExtractFromZcodeCredentials(path); err == nil {
		t.Fatal("a record sealed under another secret must be an error, not an anonymous identity")
	}
}
