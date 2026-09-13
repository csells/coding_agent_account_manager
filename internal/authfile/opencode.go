package authfile

// OpenCode keeps its logins in SQLite, not in auth.json.
//
// Current OpenCode builds store auth in $XDG_DATA_HOME/opencode/opencode.db
// (WAL mode) alongside sessions and messages: the `account` table holds the
// OpenCode Console accounts (id, email, url, access_token, refresh_token,
// token_expiry), `account_state` which of them is active, `control_account`
// the remote-control login, and `credential` every provider credential the
// old auth.json used to hold (integration_id, label, value, method_id, ...).
// auth.json is no longer written and stays in the file set only for older
// installs.
//
// The database cannot be swapped whole — it is also the user's session
// history — so the auth tables are bridged the way the Claude Desktop config
// is: Backup exports just those rows to <profile>/opencode-auth.json, Restore
// writes them back into the live database in one transaction, detection
// hashes the accounts they name, and logout empties them. The database is
// copied aside (with its WAL and shared-memory sidecars) before it is read,
// so a running OpenCode is never blocked and a half-written checkpoint is
// never read.

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	_ "modernc.org/sqlite"
)

const (
	// openCodeStoreFile is the SQLite database OpenCode keeps its auth in.
	openCodeStoreFile = "opencode.db"
	// openCodeVaultFile is the export a vault profile holds in its place.
	openCodeVaultFile = "opencode-auth.json"
)

// openCodeTables are the auth-bearing tables, in restore order (parents
// before the rows that reference them).
var openCodeTables = []string{"account", "account_state", "control_account", "credential"}

// openCodeRow is one table row, column name to value. Values are kept as
// SQLite hands them back (strings, integers, nil) so a restore writes back
// exactly what was captured.
type openCodeRow map[string]interface{}

// openCodeAuthSnapshot is the vault's export of the auth tables.
type openCodeAuthSnapshot struct {
	Tables map[string][]openCodeRow `json:"tables"`
}

// isOpenCodeStore reports whether a file-set entry is the OpenCode database.
func isOpenCodeStore(tool, path string) bool {
	return tool == "opencode" && filepath.Base(path) == openCodeStoreFile
}

// vaultFileName is the name a file-set entry is stored under in a vault
// profile: its own basename, except for the OpenCode database, whose auth
// rows are exported as JSON.
func vaultFileName(tool, path string) string {
	if isOpenCodeStore(tool, path) {
		return openCodeVaultFile
	}
	return filepath.Base(path)
}

// hasRows reports whether a snapshot carries any login at all.
func (s *openCodeAuthSnapshot) hasRows() bool {
	if s == nil {
		return false
	}
	for _, table := range []string{"account", "control_account", "credential"} {
		if len(s.Tables[table]) > 0 {
			return true
		}
	}
	return false
}

// openCodeDSN opens a database read-write with a short busy timeout; a
// running OpenCode holds the file open in WAL mode, which tolerates that.
func openCodeDSN(path string) string {
	return "file:" + path + "?_pragma=busy_timeout(5000)"
}

// copyOpenCodeStoreAside copies the database and its WAL/shared-memory
// sidecars into a temporary directory and returns the copy's path. Reading
// the copy sees every committed row without touching the live file's locks.
func copyOpenCodeStoreAside(dbPath string) (string, func(), error) {
	dir, err := os.MkdirTemp("", "caam-opencode-*")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	for _, suffix := range []string{"", "-wal", "-shm"} {
		src := dbPath + suffix
		if _, err := os.Stat(src); err != nil {
			if suffix != "" && os.IsNotExist(err) {
				continue
			}
			cleanup()
			return "", nil, err
		}
		if err := copyFile(src, filepath.Join(dir, openCodeStoreFile+suffix)); err != nil {
			cleanup()
			return "", nil, fmt.Errorf("copy %s aside: %w", src, err)
		}
	}
	return filepath.Join(dir, openCodeStoreFile), cleanup, nil
}

// exportOpenCodeAuth reads the auth tables out of the OpenCode database at
// dbPath. A table the database does not have (an older schema) is simply
// absent from the snapshot. ok is false when there is no database.
func exportOpenCodeAuth(dbPath string) (snap *openCodeAuthSnapshot, ok bool, err error) {
	if _, err := os.Stat(dbPath); err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	copyPath, cleanup, err := copyOpenCodeStoreAside(dbPath)
	if err != nil {
		return nil, false, err
	}
	defer cleanup()

	conn, err := sql.Open("sqlite", "file:"+copyPath+"?mode=ro")
	if err != nil {
		return nil, false, fmt.Errorf("open opencode.db: %w", err)
	}
	defer conn.Close()
	conn.SetMaxOpenConns(1)

	snap = &openCodeAuthSnapshot{Tables: map[string][]openCodeRow{}}
	for _, table := range openCodeTables {
		rows, err := readOpenCodeTable(conn, table)
		if err != nil {
			return nil, false, err
		}
		if rows != nil {
			snap.Tables[table] = rows
		}
	}
	return snap, true, nil
}

// readOpenCodeTable returns every row of table, nil when the table does not
// exist.
func readOpenCodeTable(conn *sql.DB, table string) ([]openCodeRow, error) {
	var name string
	err := conn.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("inspect opencode.db: %w", err)
	}
	rows, err := conn.Query(`SELECT * FROM "` + table + `"`)
	if err != nil {
		return nil, fmt.Errorf("read opencode.db %s: %w", table, err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	out := []openCodeRow{}
	for rows.Next() {
		vals := make([]interface{}, len(cols))
		ptrs := make([]interface{}, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		row := openCodeRow{}
		for i, c := range cols {
			switch v := vals[i].(type) {
			case []byte:
				row[c] = string(v)
			default:
				row[c] = v
			}
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// applyOpenCodeAuth replaces the auth tables of the database at dbPath with
// the snapshot's rows, in one transaction. Tables the snapshot does not
// carry are left alone; a table the database does not have is skipped, so a
// snapshot from a newer OpenCode restores what an older one can hold.
func applyOpenCodeAuth(dbPath string, snap *openCodeAuthSnapshot) error {
	if _, err := os.Stat(dbPath); err != nil {
		return fmt.Errorf("opencode.db not found (start OpenCode once so it creates its database): %w", err)
	}
	conn, err := sql.Open("sqlite", openCodeDSN(dbPath))
	if err != nil {
		return fmt.Errorf("open opencode.db: %w", err)
	}
	defer conn.Close()
	conn.SetMaxOpenConns(1)

	tx, err := conn.Begin()
	if err != nil {
		return fmt.Errorf("begin opencode.db transaction: %w", err)
	}
	defer tx.Rollback()

	// Clear children before parents; account_state references account.
	for i := len(openCodeTables) - 1; i >= 0; i-- {
		table := openCodeTables[i]
		if _, present := snap.Tables[table]; !present {
			continue
		}
		if exists, err := openCodeTableExists(tx, table); err != nil {
			return err
		} else if !exists {
			continue
		}
		if _, err := tx.Exec(`DELETE FROM "` + table + `"`); err != nil {
			return fmt.Errorf("clear opencode.db %s: %w", table, err)
		}
	}
	for _, table := range openCodeTables {
		rows, present := snap.Tables[table]
		if !present {
			continue
		}
		if exists, err := openCodeTableExists(tx, table); err != nil {
			return err
		} else if !exists {
			continue
		}
		for _, row := range rows {
			if err := insertOpenCodeRow(tx, table, row); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func openCodeTableExists(tx *sql.Tx, table string) (bool, error) {
	var name string
	err := tx.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect opencode.db: %w", err)
	}
	return true, nil
}

func insertOpenCodeRow(tx *sql.Tx, table string, row openCodeRow) error {
	cols := make([]string, 0, len(row))
	for c := range row {
		cols = append(cols, c)
	}
	sort.Strings(cols)
	quoted := make([]string, len(cols))
	marks := make([]string, len(cols))
	args := make([]interface{}, len(cols))
	for i, c := range cols {
		quoted[i] = `"` + c + `"`
		marks[i] = "?"
		v := row[c]
		// JSON decoding turns every number into float64; SQLite integer
		// columns (ids, timestamps) must get integers back.
		if f, ok := v.(float64); ok && f == float64(int64(f)) {
			v = int64(f)
		}
		args[i] = v
	}
	_, err := tx.Exec(`INSERT INTO "`+table+`" (`+strings.Join(quoted, ", ")+`) VALUES (`+strings.Join(marks, ", ")+`)`, args...)
	if err != nil {
		return fmt.Errorf("restore opencode.db %s: %w", table, err)
	}
	return nil
}

// clearOpenCodeAuth empties the auth tables: the OpenCode equivalent of
// removing auth.json. A missing database is nothing to clear.
func clearOpenCodeAuth(dbPath string) error {
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return nil
	}
	empty := &openCodeAuthSnapshot{Tables: map[string][]openCodeRow{}}
	for _, table := range openCodeTables {
		empty.Tables[table] = []openCodeRow{}
	}
	return applyOpenCodeAuth(dbPath, empty)
}

// readOpenCodeSnapshotFile reads a vault export.
func readOpenCodeSnapshotFile(path string) (*openCodeAuthSnapshot, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var snap openCodeAuthSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if snap.Tables == nil {
		snap.Tables = map[string][]openCodeRow{}
	}
	return &snap, nil
}

// openCodeIdentityRows reduces a snapshot to the fields that identify its
// logins: which accounts and which credentials, not their rotating tokens
// or timestamps. Both the live database and a vault export hash to this.
func openCodeIdentityRows(snap *openCodeAuthSnapshot) []string {
	var keys []string
	for _, r := range snap.Tables["account"] {
		keys = append(keys, fmt.Sprintf("account:%v:%v:%v", r["id"], r["email"], r["url"]))
	}
	for _, r := range snap.Tables["control_account"] {
		keys = append(keys, fmt.Sprintf("control:%v:%v", r["email"], r["url"]))
	}
	for _, r := range snap.Tables["credential"] {
		keys = append(keys, fmt.Sprintf("credential:%v:%v:%v:%v", r["id"], r["integration_id"], r["label"], r["method_id"]))
	}
	sort.Strings(keys)
	return keys
}

// hashOpenCodeSnapshot is the stable identity hash of a snapshot.
func hashOpenCodeSnapshot(snap *openCodeAuthSnapshot) string {
	h := sha256.New()
	h.Write([]byte("opencode:auth:"))
	for _, k := range openCodeIdentityRows(snap) {
		h.Write([]byte(k))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// hashOpenCodeStore hashes the live database's auth rows.
func hashOpenCodeStore(dbPath string) (string, error) {
	snap, ok, err := exportOpenCodeAuth(dbPath)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", os.ErrNotExist
	}
	return hashOpenCodeSnapshot(snap), nil
}

// hashOpenCodeVaultFile hashes a vault export the same way.
func hashOpenCodeVaultFile(path string) (string, error) {
	snap, err := readOpenCodeSnapshotFile(path)
	if err != nil {
		return "", err
	}
	return hashOpenCodeSnapshot(snap), nil
}

// openCodeStoreHasAuth reports whether the live database holds any login.
func openCodeStoreHasAuth(dbPath string) bool {
	snap, ok, err := exportOpenCodeAuth(dbPath)
	return err == nil && ok && snap.hasRows()
}

// openCodeSnapshotIdentity returns the email of the active Console account
// (or the first one, or the control account) in a snapshot, "" when none.
func openCodeSnapshotIdentity(snap *openCodeAuthSnapshot) string {
	if snap == nil {
		return ""
	}
	accounts := snap.Tables["account"]
	var activeID interface{}
	for _, st := range snap.Tables["account_state"] {
		if id, ok := st["active_account_id"]; ok && id != nil {
			activeID = id
		}
	}
	for _, a := range accounts {
		if activeID != nil && fmt.Sprint(a["id"]) == fmt.Sprint(activeID) {
			return jsonString(a, "email")
		}
	}
	if len(accounts) > 0 {
		return jsonString(accounts[0], "email")
	}
	for _, c := range snap.Tables["control_account"] {
		if email := jsonString(c, "email"); email != "" {
			return email
		}
	}
	return ""
}

// openCodeProfileIdentity extracts the identity of an OpenCode vault
// profile from its export, or from a legacy auth.json.
func (v *Vault) openCodeProfileIdentity(profileDir string) string {
	if snap, err := readOpenCodeSnapshotFile(filepath.Join(profileDir, openCodeVaultFile)); err == nil {
		if email := openCodeSnapshotIdentity(snap); email != "" {
			return email
		}
	}
	return profileMetaIdentity(profileDir)
}
