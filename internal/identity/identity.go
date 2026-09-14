// Package identity extracts account identity details from provider auth artifacts.
package identity

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// MetaIdentity reads the identity `caam backup` (or RecordProfileIdentity)
// recorded in a vault profile's meta.json, "" when the file is missing,
// unreadable, not JSON, or carries no identity.
func MetaIdentity(dir string) string {
	data, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if err != nil {
		return ""
	}
	var meta struct {
		Identity string `json:"identity"`
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		return ""
	}
	return strings.TrimSpace(meta.Identity)
}

// Identity captures account metadata extracted from auth files.
type Identity struct {
	Email        string    `json:"email,omitempty"`
	Organization string    `json:"organization,omitempty"`
	PlanType     string    `json:"plan_type,omitempty"`
	AccountID    string    `json:"account_id,omitempty"`
	ExpiresAt    time.Time `json:"expires_at,omitempty"`
	Provider     string    `json:"provider"`
}
