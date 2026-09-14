package identity

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExtractFromAgyProfile(t *testing.T) {
	dir := t.TempDir()
	if _, err := ExtractFromAgyProfile(dir); err == nil {
		t.Fatal("an empty directory must yield no identity")
	}

	token := `{"auth_method":"oauth","token":{"access_token":"SYNTHETIC","refresh_token":"SYNTHETIC","expiry":"2030-01-01T12:00:00.5Z"}}`
	if err := os.WriteFile(filepath.Join(dir, "antigravity-oauth-token"), []byte(token), 0o600); err != nil {
		t.Fatal(err)
	}
	id, err := ExtractFromAgyProfile(dir)
	if err != nil {
		t.Fatalf("token only: %v", err)
	}
	if id.Provider != "agy" || id.Email != "" || id.ExpiresAt.Year() != 2030 {
		t.Errorf("token only = %+v", id)
	}

	if err := os.WriteFile(filepath.Join(dir, "google_accounts.json"), []byte(`{"active":"work@example.com","old":["x@example.com"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	id, err = ExtractFromAgyProfile(dir)
	if err != nil {
		t.Fatalf("both files: %v", err)
	}
	if id.Email != "work@example.com" {
		t.Errorf("Email = %q, want the active Google account", id.Email)
	}

	// A profile whose google_accounts.json names no active account falls
	// back to the identity recorded in meta.json.
	if err := os.WriteFile(filepath.Join(dir, "google_accounts.json"), []byte(`{"active":null,"old":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), []byte(`{"tool":"agy","identity":"chris@example.com"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	id, err = ExtractFromAgyProfile(dir)
	if err != nil {
		t.Fatalf("meta fallback: %v", err)
	}
	if id.Email != "chris@example.com" {
		t.Errorf("Email = %q, want the recorded identity", id.Email)
	}
}

// google_accounts.json is the Gemini CLI's file: its "active" account is
// whoever the Gemini CLI last used, which need not be the Antigravity
// account this profile holds. The identity caam recorded for the profile
// in meta.json wins over it.
func TestExtractFromAgyProfile_MetaIdentityWinsOverGeminisAccountsFile(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("google_accounts.json", `{"active":"gemini-user@example.com","old":[]}`)
	write("meta.json", `{"identity":"agy-user@example.com"}`)
	id, err := ExtractFromAgyProfile(dir)
	if err != nil {
		t.Fatal(err)
	}
	if id.Email != "agy-user@example.com" {
		t.Fatalf("Email = %q, want the profile's recorded identity, not the Gemini CLI's active account", id.Email)
	}
}
