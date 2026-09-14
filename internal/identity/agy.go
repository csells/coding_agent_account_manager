package identity

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ExtractFromAgyProfile reads an Antigravity CLI auth directory — a vault
// profile, or the live ~/.gemini tree laid out the same way — and extracts
// identity.
//
// The token file (antigravity-oauth-token) carries no identity of its own,
// only the oauth2 token with its expiry; the active Google account is
// recorded beside it in google_accounts.json ({"active": "<email>", ...}).
// google_accounts.json is the legacy Gemini CLI's and may name no active
// account; a vault profile then carries the email recorded in its meta.json
// by `caam backup agy`. Either file alone still yields an identity: an email
// without an expiry, or an expiry without an email.
func ExtractFromAgyProfile(dir string) (*Identity, error) {
	id := &Identity{Provider: "agy"}
	found := false

	// The account caam recorded for this profile (from Google's userinfo,
	// or the email the login reported) wins: google_accounts.json is the
	// Gemini CLI's file, and its active account is whoever the Gemini CLI
	// last used, which need not be this Antigravity account.
	if email := MetaIdentity(dir); strings.Contains(email, "@") {
		id.Email = email
		found = true
	}

	if id.Email == "" {
		if data, err := os.ReadFile(filepath.Join(dir, "google_accounts.json")); err == nil {
			var accounts struct {
				Active string `json:"active"`
			}
			if err := json.Unmarshal(data, &accounts); err == nil {
				id.Email = strings.TrimSpace(accounts.Active)
				found = found || id.Email != ""
			}
		}
	}

	if data, err := os.ReadFile(filepath.Join(dir, "antigravity-oauth-token")); err == nil {
		var root struct {
			Token struct {
				Expiry string `json:"expiry"`
			} `json:"token"`
		}
		if err := json.Unmarshal(data, &root); err == nil {
			found = true
			if exp, err := time.Parse(time.RFC3339Nano, root.Token.Expiry); err == nil {
				id.ExpiresAt = exp
			}
		}
	}

	if !found {
		return nil, fmt.Errorf("no antigravity auth in %s", dir)
	}
	return id, nil
}
