package identity

import (
	"fmt"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/zcodecred"
)

// ExtractFromZcodeCredentials unseals a zcode credential record and
// extracts identity: the signed-in Z.ai user's email, name and id from the
// user profile zcode stores beside its tokens, and the Z.ai access token's
// expiry. The record is readable only under the user's own secret; a
// record sealed elsewhere is an error, not an anonymous identity.
func ExtractFromZcodeCredentials(path string) (*Identity, error) {
	rec, err := zcodecred.ReadRecord(path)
	if err != nil {
		return nil, fmt.Errorf("read zcode credentials: %w", err)
	}
	if !rec.LoggedIn() {
		return nil, fmt.Errorf("zcode credentials hold no session (logged out)")
	}
	id := &Identity{Provider: "zcode"}
	if rec.UserInfo != nil {
		id.Email = rec.UserInfo.Email
		id.AccountID = rec.UserInfo.UserID
		id.Organization = rec.UserInfo.Name
	}
	if jwtID, err := ExtractFromJWT(rec.AccessToken); err == nil && jwtID != nil {
		id.ExpiresAt = jwtID.ExpiresAt
		if id.AccountID == "" {
			id.AccountID = jwtID.AccountID
		}
	}
	return id, nil
}
