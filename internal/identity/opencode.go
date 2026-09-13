package identity

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// ExtractFromOpenCodeExport reads a vault export of OpenCode's login tables
// (opencode-auth.json, written by `caam backup opencode` from opencode.db)
// and extracts identity: the active Console account's email (else the
// first account's, else the control account's) and its token expiry.
func ExtractFromOpenCodeExport(path string) (*Identity, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read opencode export: %w", err)
	}
	var export struct {
		Tables struct {
			Account []struct {
				ID          string      `json:"id"`
				Email       string      `json:"email"`
				TokenExpiry json.Number `json:"token_expiry"`
			} `json:"account"`
			AccountState []struct {
				ActiveAccountID string `json:"active_account_id"`
			} `json:"account_state"`
			ControlAccount []struct {
				Email string `json:"email"`
			} `json:"control_account"`
		} `json:"tables"`
	}
	if err := json.Unmarshal(data, &export); err != nil {
		return nil, fmt.Errorf("parse opencode export: %w", err)
	}
	id := &Identity{Provider: "opencode"}
	t := export.Tables
	pick := -1
	for _, st := range t.AccountState {
		for i, a := range t.Account {
			if a.ID == st.ActiveAccountID {
				pick = i
			}
		}
	}
	if pick < 0 && len(t.Account) > 0 {
		pick = 0
	}
	if pick >= 0 {
		id.Email = t.Account[pick].Email
		id.AccountID = t.Account[pick].ID
		if ms, err := t.Account[pick].TokenExpiry.Int64(); err == nil && ms > 0 {
			id.ExpiresAt = time.UnixMilli(ms)
		}
	} else if len(t.ControlAccount) > 0 {
		id.Email = t.ControlAccount[0].Email
	}
	if id.Email == "" && id.AccountID == "" {
		return nil, fmt.Errorf("no account in opencode export")
	}
	return id, nil
}
