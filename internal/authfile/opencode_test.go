package authfile

// OpenCode stores its logins in opencode.db (switcher handoff, work item G).
// Every database here is a SYNTHETIC copy of OpenCode's schema; no test
// touches the real ~/.local/share/opencode.

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// openCodeSchema is OpenCode's auth schema as of 1.18 (verified against a
// live database), trimmed to the columns caam cares about plus a session
// table that must survive every operation untouched.
const openCodeSchema = `
CREATE TABLE account (id text PRIMARY KEY, email text NOT NULL, url text NOT NULL, access_token text NOT NULL, refresh_token text NOT NULL, token_expiry integer, time_created integer NOT NULL, time_updated integer NOT NULL);
CREATE TABLE account_state (id integer PRIMARY KEY NOT NULL, active_account_id text, active_org_id text, FOREIGN KEY (active_account_id) REFERENCES account(id) ON DELETE SET NULL);
CREATE TABLE control_account (email text NOT NULL, url text NOT NULL, access_token text NOT NULL, refresh_token text NOT NULL, token_expiry integer, active integer NOT NULL, time_created integer NOT NULL, time_updated integer NOT NULL, CONSTRAINT control_account_pk PRIMARY KEY(email, url));
CREATE TABLE credential (id text PRIMARY KEY, integration_id text, label text NOT NULL, value text NOT NULL, connector_id text, method_id text, active integer, time_created integer NOT NULL, time_updated integer NOT NULL);
CREATE TABLE session (id text PRIMARY KEY, title text NOT NULL);
INSERT INTO session VALUES ('sess_1', 'do not lose me');
`

func newOpenCodeStore(t *testing.T) (AuthFileSet, string) {
	t.Helper()
	dataHome := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dataHome)
	fs := OpenCodeAuthFiles()
	dbPath := fs.Files[0].Path
	if filepath.Base(dbPath) != "opencode.db" || !fs.Files[0].Required {
		t.Fatalf("OpenCodeAuthFiles = %+v", fs)
	}
	if err := os.MkdirAll(filepath.Dir(dbPath), 0700); err != nil {
		t.Fatal(err)
	}
	conn, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Exec(openCodeSchema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	return fs, dbPath
}

func openCodeLogin(t *testing.T, dbPath, id, email, token string) {
	t.Helper()
	conn, err := sql.Open("sqlite", openCodeDSN(dbPath))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	stmts := []string{
		`DELETE FROM account_state`, `DELETE FROM account`, `DELETE FROM credential`,
		`INSERT INTO account VALUES ('` + id + `','` + email + `','https://opencode.ai/console','SYNTHETIC-AT-` + token + `','SYNTHETIC-RT-` + token + `',1893456000000,1,2)`,
		`INSERT INTO account_state VALUES (1,'` + id + `','org_1')`,
		`INSERT INTO credential VALUES ('cred_` + id + `','opencode','OpenCode Zen','{"type":"api","key":"SYNTHETIC-ZEN-` + token + `"}',NULL,'api',1,1,2)`,
	}
	for _, s := range stmts {
		if _, err := conn.Exec(s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
}

func openCodeRowCount(t *testing.T, dbPath, table string) int {
	t.Helper()
	conn, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var n int
	if err := conn.QueryRow(`SELECT count(*) FROM "` + table + `"`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestOpenCode_EmptyStoreIsNotALogin(t *testing.T) {
	fs, dbPath := newOpenCodeStore(t)
	if HasAuthFiles(fs) {
		t.Fatal("an OpenCode database with no rows was reported as a login")
	}
	v := NewVault(t.TempDir())
	err := v.Backup(fs, "work")
	if err == nil {
		t.Fatal("Backup captured an empty OpenCode store")
	}
	if !strings.Contains(err.Error(), dbPath) || !strings.Contains(err.Error(), "no account or credential rows") {
		t.Errorf("Backup error does not name the store and the reason: %v", err)
	}
	if active, _ := v.ActiveProfile(fs); active != "" {
		t.Errorf("ActiveProfile on an empty store = %q", active)
	}
}

func TestOpenCode_BackupExportsRowsAndRestoreAppliesThem(t *testing.T) {
	fs, dbPath := newOpenCodeStore(t)
	v := NewVault(t.TempDir())

	openCodeLogin(t, dbPath, "acc_a", "a@example.com", "a")
	if !HasAuthFiles(fs) {
		t.Fatal("HasAuthFiles missed a login stored in opencode.db")
	}
	if err := v.Backup(fs, "a"); err != nil {
		t.Fatalf("Backup(a): %v", err)
	}
	export := filepath.Join(v.ProfilePath("opencode", "a"), "opencode-auth.json")
	snap, err := readOpenCodeSnapshotFile(export)
	if err != nil {
		t.Fatalf("read export: %v", err)
	}
	if len(snap.Tables["account"]) != 1 || len(snap.Tables["credential"]) != 1 || len(snap.Tables["account_state"]) != 1 {
		t.Fatalf("export = %+v", snap.Tables)
	}
	if _, err := os.Stat(filepath.Join(v.ProfilePath("opencode", "a"), "opencode.db")); err == nil {
		t.Fatal("the whole database was copied into the vault")
	}
	if id := v.ProfileIdentity("opencode", "a"); id != "a@example.com" {
		t.Errorf("ProfileIdentity = %q", id)
	}
	if active, _ := v.ActiveProfile(fs); active != "a" {
		t.Fatalf("ActiveProfile = %q, want a", active)
	}

	openCodeLogin(t, dbPath, "acc_b", "b@example.com", "b")
	if err := v.Backup(fs, "b"); err != nil {
		t.Fatalf("Backup(b): %v", err)
	}
	if active, _ := v.ActiveProfile(fs); active != "b" {
		t.Fatalf("ActiveProfile = %q, want b", active)
	}

	if err := v.Restore(fs, "a"); err != nil {
		t.Fatalf("Restore(a): %v", err)
	}
	live, ok, err := exportOpenCodeAuth(dbPath)
	if err != nil || !ok {
		t.Fatal(err)
	}
	if got := jsonString(live.Tables["account"][0], "email"); got != "a@example.com" {
		t.Fatalf("live account after Restore(a) = %q", got)
	}
	if got := jsonString(live.Tables["credential"][0], "value"); !strings.Contains(got, "SYNTHETIC-ZEN-a") {
		t.Fatalf("live credential after Restore(a) = %q", got)
	}
	if got := jsonString(live.Tables["account_state"][0], "active_account_id"); got != "acc_a" {
		t.Fatalf("active account after Restore(a) = %q", got)
	}
	if active, _ := v.ActiveProfile(fs); active != "a" {
		t.Fatalf("ActiveProfile after Restore(a) = %q", active)
	}
	// The session history was never touched.
	if n := openCodeRowCount(t, dbPath, "session"); n != 1 {
		t.Fatalf("session rows = %d, want 1", n)
	}

	// OpenCode rotates the tokens in place; detection follows the account.
	conn, _ := sql.Open("sqlite", openCodeDSN(dbPath))
	if _, err := conn.Exec(`UPDATE account SET access_token='ROTATED', refresh_token='ROTATED', token_expiry=1893459600000, time_updated=3`); err != nil {
		t.Fatal(err)
	}
	conn.Close()
	if active, _ := v.ActiveProfile(fs); active != "a" {
		t.Fatalf("ActiveProfile after token rotation = %q", active)
	}

	if err := ClearAuthFiles(fs); err != nil {
		t.Fatalf("ClearAuthFiles: %v", err)
	}
	if HasAuthFiles(fs) {
		t.Fatal("still logged in after ClearAuthFiles")
	}
	if _, err := os.Stat(dbPath); err != nil {
		t.Fatal("ClearAuthFiles removed the database")
	}
	if n := openCodeRowCount(t, dbPath, "session"); n != 1 {
		t.Fatalf("session rows after clear = %d, want 1", n)
	}
}

// TestOpenCode_LegacyAuthJSONStillWorks: an older OpenCode with no database
// and an auth.json keeps backing up and restoring as before.
func TestOpenCode_LegacyAuthJSONStillWorks(t *testing.T) {
	dataHome := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dataHome)
	fs := OpenCodeAuthFiles()
	authPath := fs.Files[1].Path
	if filepath.Base(authPath) != "auth.json" || fs.Files[1].Required || !fs.AllowOptionalOnly {
		t.Fatalf("legacy entry = %+v", fs)
	}
	if err := os.MkdirAll(filepath.Dir(authPath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(authPath, []byte(`{"anthropic":{"type":"api","key":"SYNTHETIC"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if !HasAuthFiles(fs) {
		t.Fatal("legacy auth.json not recognised")
	}
	v := NewVault(t.TempDir())
	if err := v.Backup(fs, "legacy"); err != nil {
		t.Fatalf("Backup: %v", err)
	}
	if _, err := os.Stat(filepath.Join(v.ProfilePath("opencode", "legacy"), "auth.json")); err != nil {
		t.Fatalf("auth.json not vaulted: %v", err)
	}
	if err := v.Restore(fs, "legacy"); err != nil {
		t.Fatalf("Restore: %v", err)
	}
}
