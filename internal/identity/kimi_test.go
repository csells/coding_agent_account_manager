package identity

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// syntheticJWT builds an unsigned JWT with the given claims (signature is
// never checked by the extractor).
func syntheticJWT(t *testing.T, claims map[string]any) string {
	t.Helper()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	return header + "." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"
}

func TestExtractFromKimiCredentials(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "kimi-code.json")

	// Logged out: the CLI leaves empty tokens behind.
	if err := os.WriteFile(path, []byte(`{"access_token":"","refresh_token":"","expires_at":0}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ExtractFromKimiCredentials(path); err == nil {
		t.Fatal("a logged-out file must yield no identity")
	}

	// An opaque token with a recorded identity beside it.
	if err := os.WriteFile(path, []byte(`{"access_token":"SYNTHETIC-OPAQUE","refresh_token":"SYNTHETIC-RT","expires_at":1893456000}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), []byte(`{"identity":"dev@example.com"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	id, err := ExtractFromKimiCredentials(path)
	if err != nil {
		t.Fatalf("opaque token: %v", err)
	}
	if id.Provider != "kimi" || id.Email != "dev@example.com" || id.ExpiresAt.UTC().Year() != 2030 {
		t.Errorf("opaque token identity = %+v", id)
	}

	// A JWT token carries its own claims, which win.
	jwt := syntheticJWT(t, map[string]any{"email": "jwt@example.com", "sub": "u-1", "exp": 1893456000})
	if err := os.WriteFile(path, []byte(`{"access_token":"`+jwt+`","refresh_token":"x","expires_at":1893456000}`), 0o600); err != nil {
		t.Fatal(err)
	}
	id, err = ExtractFromKimiCredentials(path)
	if err != nil {
		t.Fatalf("jwt token: %v", err)
	}
	if id.Email != "jwt@example.com" {
		t.Errorf("jwt identity = %+v", id)
	}
}
