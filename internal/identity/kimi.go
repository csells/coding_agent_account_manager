package identity

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ExtractFromKimiCredentials reads a Kimi Code token file
// (credentials/kimi-code.json) and extracts identity.
//
// The file is a plain OAuth token: {"access_token","refresh_token",
// "expires_at" (epoch seconds),...}. The access token may be a JWT carrying
// account claims; when it is not, the account is only known to Kimi, and
// `caam backup kimi` records what Kimi's /me reported in the profile's
// meta.json, which is read as the fallback. A file with empty tokens is the
// CLI's logged-out state and yields no identity.
func ExtractFromKimiCredentials(path string) (*Identity, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read kimi credentials: %w", err)
	}
	var creds struct {
		AccessToken  string      `json:"access_token"`
		RefreshToken string      `json:"refresh_token"`
		ExpiresAt    json.Number `json:"expires_at"`
	}
	if err := json.Unmarshal(data, &creds); err != nil {
		return nil, fmt.Errorf("parse kimi credentials: %w", err)
	}
	if strings.TrimSpace(creds.AccessToken) == "" && strings.TrimSpace(creds.RefreshToken) == "" {
		return nil, fmt.Errorf("kimi credentials hold no token (logged out)")
	}

	id := &Identity{Provider: "kimi"}
	if jwtID, err := ExtractFromJWT(creds.AccessToken); err == nil && jwtID != nil {
		id.Email = jwtID.Email
		id.AccountID = jwtID.AccountID
		id.PlanType = jwtID.PlanType
	}
	if secs, err := creds.ExpiresAt.Float64(); err == nil && secs > 0 {
		id.ExpiresAt = time.Unix(int64(secs), 0)
	}
	if id.Email == "" {
		id.Email = MetaIdentity(filepath.Dir(path))
	}
	return id, nil
}
