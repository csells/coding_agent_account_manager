package identity

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExtractFromOpenCodeExport(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "opencode-auth.json")
	write := func(content string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(`{"tables":{"account":[],"credential":[{"id":"c1","integration_id":"anthropic","label":"x","value":"y"}]}}`)
	if _, err := ExtractFromOpenCodeExport(path); err == nil {
		t.Fatal("an export with no account yielded an identity")
	}
	write(`{"tables":{"account":[{"id":"acc_1","email":"first@example.com","token_expiry":1893456000000},{"id":"acc_2","email":"active@example.com","token_expiry":1893459600000}],"account_state":[{"id":1,"active_account_id":"acc_2"}]}}`)
	id, err := ExtractFromOpenCodeExport(path)
	if err != nil {
		t.Fatal(err)
	}
	if id.Provider != "opencode" || id.Email != "active@example.com" || id.AccountID != "acc_2" || id.ExpiresAt.UnixMilli() != 1893459600000 {
		t.Errorf("identity = %+v", id)
	}
	write(`{"tables":{"control_account":[{"email":"control@example.com"}]}}`)
	if id, err := ExtractFromOpenCodeExport(path); err != nil || id.Email != "control@example.com" {
		t.Errorf("control-only export = %+v, %v", id, err)
	}
}
