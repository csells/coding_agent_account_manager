package authfile

// Tests for issue #98: on macOS the live Claude OAuth blob is a login-keychain
// item, not a file, so backup captured a token-less profile and activate was a
// silent no-op. The keychain is bridged to ~/.claude/.credentials.json.

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/keychain"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/testutil"
)

// keychainFixture is a Claude file set whose credentials file starts absent,
// as it is on a Mac, backed by a fake keychain.
type keychainFixture struct {
	t         *testing.T
	vault     *Vault
	vaultDir  string
	fileSet   AuthFileSet
	items     string
	credPath  string
	statePath string
}

func newKeychainFixture(t *testing.T) *keychainFixture {
	t.Helper()
	items := testutil.FakeKeychain(t)

	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0700); err != nil {
		t.Fatal(err)
	}
	// The bridge only applies to the credentials file under the current HOME,
	// so the fixture has to own it.
	t.Setenv("HOME", home)
	f := &keychainFixture{
		t:         t,
		vaultDir:  filepath.Join(tmp, "vault"),
		items:     items,
		credPath:  filepath.Join(home, ".claude", ".credentials.json"),
		statePath: filepath.Join(home, ".claude.json"),
	}
	f.vault = NewVault(f.vaultDir)
	f.fileSet = AuthFileSet{
		Tool: "claude",
		Files: []AuthFileSpec{
			{Tool: "claude", Path: f.credPath, Required: true},
			{Tool: "claude", Path: f.statePath, Required: false},
		},
		AllowOptionalOnly: true,
	}
	return f
}

func (f *keychainFixture) storeToken(blob string) {
	f.t.Helper()
	testutil.FakeKeychainStore(f.t, f.items, keychain.ClaudeService, keychain.LoginAccount(), blob)
}

func (f *keychainFixture) storedToken() (string, bool) {
	f.t.Helper()
	return testutil.FakeKeychainRead(f.t, f.items, keychain.ClaudeService)
}

func keychainCreds(access string) string {
	return `{"claudeAiOauth":{"accessToken":"` + access + `","refreshToken":"rt-` + access + `","expiresAt":1893456000000}}`
}

func keychainState(email string) string {
	raw, err := json.Marshal(map[string]any{
		"numStartups":  3,
		"oauthAccount": map[string]any{"emailAddress": email, "accountUuid": "acct-" + email},
	})
	if err != nil {
		panic(err)
	}
	return string(raw)
}

// TestBackupCapturesKeychainToken is the headline of #98: with the token only
// in the keychain, the vault profile must still get a .credentials.json.
func TestBackupCapturesKeychainToken(t *testing.T) {
	f := newKeychainFixture(t)
	f.storeToken(keychainCreds("at-live"))
	writeFixtureFile(t, f.statePath, keychainState("alice@example.com"))

	if err := f.vault.Backup(f.fileSet, "alice"); err != nil {
		t.Fatalf("Backup: %v", err)
	}

	saved := filepath.Join(f.vaultDir, "claude", "alice", ".credentials.json")
	if got := readFixtureFile(t, saved); got != keychainCreds("at-live") {
		t.Fatalf("vault credentials = %q, want the keychain payload", got)
	}
	// The mirror is left in place at 0600 so hashing and expiry keep working.
	info, err := os.Stat(f.credPath)
	if err != nil {
		t.Fatalf("stat mirror: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("mirror mode = %o, want 600", perm)
	}
	// meta.json records the account identity, so `caam ls` is not "unknown".
	meta := readFixtureFile(t, filepath.Join(f.vaultDir, "claude", "alice", "meta.json"))
	if !strings.Contains(meta, "alice@example.com") {
		t.Fatalf("meta.json carries no identity: %s", meta)
	}
}

// TestBackupFailsWhenKeychainRefuses: a token-less profile is worse than a
// failed backup, so a locked keychain must be an error, not a silent success.
func TestBackupFailsWhenKeychainRefuses(t *testing.T) {
	f := newKeychainFixture(t)
	f.storeToken(keychainCreds("at-live"))
	writeFixtureFile(t, f.statePath, keychainState("alice@example.com"))
	t.Setenv("CAAM_FAKE_KEYCHAIN_LOCKED", "1")

	err := f.vault.Backup(f.fileSet, "alice")
	if err == nil {
		t.Fatal("Backup succeeded against a locked keychain")
	}
	if !strings.Contains(err.Error(), "keychain") {
		t.Fatalf("Backup error does not name the keychain: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(f.vaultDir, "claude", "alice", ".credentials.json")); statErr == nil {
		t.Fatal("Backup wrote a profile despite the keychain failure")
	}
}

// TestRestorePushesTokenToKeychain: activate only changes the account once the
// snapshot is back in the keychain.
func TestRestorePushesTokenToKeychain(t *testing.T) {
	f := newKeychainFixture(t)

	// Two accounts backed up while each was live.
	f.storeToken(keychainCreds("at-alice"))
	writeFixtureFile(t, f.statePath, keychainState("alice@example.com"))
	if err := f.vault.Backup(f.fileSet, "alice"); err != nil {
		t.Fatalf("Backup alice: %v", err)
	}

	f.storeToken(keychainCreds("at-bob"))
	writeFixtureFile(t, f.statePath, keychainState("bob@example.com"))
	if err := f.vault.Backup(f.fileSet, "bob"); err != nil {
		t.Fatalf("Backup bob: %v", err)
	}

	if err := f.vault.Restore(f.fileSet, "alice"); err != nil {
		t.Fatalf("Restore alice: %v", err)
	}
	stored, ok := f.storedToken()
	if !ok {
		t.Fatal("Restore left no keychain item")
	}
	if stored != keychainCreds("at-alice") {
		t.Fatalf("keychain holds %q after activating alice", stored)
	}
	if got := readFixtureFile(t, f.credPath); got != keychainCreds("at-alice") {
		t.Fatalf("mirror holds %q after activating alice", got)
	}
	if name := f.active(); name != "alice" {
		t.Fatalf("ActiveProfile = %q, want alice", name)
	}
}

// TestRestoreFailsWhenKeychainRefuses: reporting a successful switch while the
// keychain still holds the previous account is exactly the bug.
func TestRestoreFailsWhenKeychainRefuses(t *testing.T) {
	f := newKeychainFixture(t)
	f.storeToken(keychainCreds("at-alice"))
	writeFixtureFile(t, f.statePath, keychainState("alice@example.com"))
	if err := f.vault.Backup(f.fileSet, "alice"); err != nil {
		t.Fatalf("Backup: %v", err)
	}

	t.Setenv("CAAM_FAKE_KEYCHAIN_LOCKED", "1")
	err := f.vault.Restore(f.fileSet, "alice")
	if err == nil {
		t.Fatal("Restore reported success against a locked keychain")
	}
	if !strings.Contains(err.Error(), "keychain") {
		t.Fatalf("Restore error does not name the keychain: %v", err)
	}
}

// TestHasAuthFilesSeesKeychainOnlyLogin keeps callers from routing a logged-in
// Mac down the login path.
func TestHasAuthFilesSeesKeychainOnlyLogin(t *testing.T) {
	f := newKeychainFixture(t)

	if HasAuthFiles(f.fileSet) {
		t.Fatal("HasAuthFiles reported a login with nothing present")
	}
	f.storeToken(keychainCreds("at-live"))
	if !HasAuthFiles(f.fileSet) {
		t.Fatal("HasAuthFiles missed a keychain-only login")
	}
}

// TestClearAuthFilesRemovesKeychainItem: removing the mirror is not a logout
// while the keychain still holds the token Claude Code prefers.
func TestClearAuthFilesRemovesKeychainItem(t *testing.T) {
	f := newKeychainFixture(t)
	f.storeToken(keychainCreds("at-live"))
	writeFixtureFile(t, f.credPath, keychainCreds("at-live"))

	if err := ClearAuthFiles(f.fileSet); err != nil {
		t.Fatalf("ClearAuthFiles: %v", err)
	}
	if _, ok := f.storedToken(); ok {
		t.Fatal("ClearAuthFiles left the keychain item behind")
	}
	if _, err := os.Stat(f.credPath); !os.IsNotExist(err) {
		t.Fatal("ClearAuthFiles left the mirror behind")
	}
}

// TestBridgeIsInertWhenDisabled covers the Linux/shallow-profile path: with no
// login keychain, everything falls back to files exactly as before.
func TestBridgeIsInertWhenDisabled(t *testing.T) {
	f := newKeychainFixture(t)
	f.storeToken(keychainCreds("at-keychain"))
	t.Setenv("CAAM_KEYCHAIN", "0")

	writeFixtureFile(t, f.credPath, keychainCreds("at-file"))
	writeFixtureFile(t, f.statePath, keychainState("alice@example.com"))
	if err := f.vault.Backup(f.fileSet, "alice"); err != nil {
		t.Fatalf("Backup: %v", err)
	}
	saved := filepath.Join(f.vaultDir, "claude", "alice", ".credentials.json")
	if got := readFixtureFile(t, saved); got != keychainCreds("at-file") {
		t.Fatalf("vault credentials = %q, want the on-disk file", got)
	}
	if err := f.vault.Restore(f.fileSet, "alice"); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if stored, _ := f.storedToken(); stored != keychainCreds("at-keychain") {
		t.Fatalf("disabled bridge wrote the keychain: %q", stored)
	}
}

func (f *keychainFixture) active() string {
	f.t.Helper()
	name, err := f.vault.ActiveProfile(f.fileSet)
	if err != nil {
		f.t.Fatalf("ActiveProfile: %v", err)
	}
	return name
}

// TestBackupRefusesTokenlessClaudeSnapshot: a Mac with no keychain item and
// no credentials file used to back up "successfully" — settings and session
// state, no token — and `caam limits` then found nothing to present. A
// Required credential that cannot be obtained must be an error that names
// the item it looked for.
func TestBackupRefusesTokenlessClaudeSnapshot(t *testing.T) {
	f := newKeychainFixture(t)
	// The fake keychain holds no Claude item at all; the state file alone
	// carries an identity, which is what made the old profile look complete.
	writeFixtureFile(t, f.statePath, keychainState("alice@example.com"))

	err := f.vault.Backup(f.fileSet, "alice")
	if err == nil {
		t.Fatal("Backup succeeded with no credential anywhere")
	}
	for _, want := range []string{f.credPath, keychain.ClaudeService, "/login"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Backup error %q does not mention %q", err, want)
		}
	}
	if _, statErr := os.Stat(filepath.Join(f.vaultDir, "claude", "alice", "meta.json")); statErr == nil {
		t.Fatal("Backup wrote profile metadata despite having no credential")
	}
}

// TestBackupAcceptsAPIKeyModeWithoutKeychainItem: API-key mode has no OAuth
// blob to find; settings.json is the credential and the snapshot stays valid.
func TestBackupAcceptsAPIKeyModeWithoutKeychainItem(t *testing.T) {
	f := newKeychainFixture(t)
	settingsPath := filepath.Join(filepath.Dir(f.credPath), "settings.json")
	f.fileSet.Files = append(f.fileSet.Files, AuthFileSpec{Tool: "claude", Path: settingsPath, Required: false})
	writeFixtureFile(t, settingsPath, `{"apiKeyHelper":"/usr/local/bin/key-helper","enabledPlugins":{}}`)
	writeFixtureFile(t, f.statePath, keychainState("alice@example.com"))

	if err := f.vault.Backup(f.fileSet, "alice"); err != nil {
		t.Fatalf("Backup rejected an API-key-mode login: %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.vaultDir, "claude", "alice", "settings.json")); err != nil {
		t.Fatalf("settings.json not vaulted: %v", err)
	}
}

// --- Antigravity ------------------------------------------------------------
//
// agy keeps its Google OAuth token in the login keychain (service "gemini",
// account "antigravity", via go-keyring) and, on a Mac, never writes
// antigravity-oauth-token; the file set used to look only for the file, so
// backup failed with "no auth files found" (switcher handoff, work item C).

type agyKeychainFixture struct {
	t            *testing.T
	vault        *Vault
	vaultDir     string
	fileSet      AuthFileSet
	items        string
	tokenPath    string
	accountsPath string
}

func newAgyKeychainFixture(t *testing.T) *agyKeychainFixture {
	t.Helper()
	items := testutil.FakeKeychain(t)

	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	if err := os.MkdirAll(filepath.Join(home, ".gemini", "antigravity-cli"), 0700); err != nil {
		t.Fatal(err)
	}
	// The bridge applies to the token under the current HOME's ~/.gemini
	// only, so the fixture has to own HOME and leave GEMINI_HOME unset.
	t.Setenv("HOME", home)
	t.Setenv("GEMINI_HOME", "")
	f := &agyKeychainFixture{
		t:            t,
		vaultDir:     filepath.Join(tmp, "vault"),
		items:        items,
		tokenPath:    filepath.Join(home, ".gemini", "antigravity-cli", "antigravity-oauth-token"),
		accountsPath: filepath.Join(home, ".gemini", "google_accounts.json"),
	}
	f.vault = NewVault(f.vaultDir)
	f.fileSet = AntigravityAuthFiles()
	if got := f.fileSet.Files[0].Path; got != f.tokenPath {
		t.Fatalf("fixture token path = %q, want %q", got, f.tokenPath)
	}
	return f
}

// storeToken files the token the way agy does: through go-keyring, which
// base64-wraps a value ending in a newline before handing it to `security`.
func (f *agyKeychainFixture) storeToken(blob string) {
	f.t.Helper()
	testutil.FakeKeychainStore(f.t, f.items, keychain.AgyService, keychain.AgyAccount,
		"go-keyring-base64:"+base64.StdEncoding.EncodeToString([]byte(blob)))
}

func (f *agyKeychainFixture) storedToken() (string, bool) {
	f.t.Helper()
	return testutil.FakeKeychainRead(f.t, f.items, keychain.AgyService)
}

func agyKeychainToken(tag string) string {
	return `{"auth_method":"oauth","token":{"access_token":"SYNTHETIC-` + tag + `","refresh_token":"SYNTHETIC-RT-` + tag + `","expiry":"2030-01-01T00:00:00Z"}}` + "\n"
}

func TestAgyBackupCapturesKeychainToken(t *testing.T) {
	f := newAgyKeychainFixture(t)
	f.storeToken(agyKeychainToken("alice"))
	writeFixtureFile(t, f.accountsPath, `{"active":"alice@example.com","old":[]}`)

	if !HasAuthFiles(f.fileSet) {
		t.Fatal("HasAuthFiles missed a keychain-only agy login")
	}
	if err := f.vault.Backup(f.fileSet, "alice"); err != nil {
		t.Fatalf("Backup: %v", err)
	}
	saved := filepath.Join(f.vaultDir, "agy", "alice", "antigravity-oauth-token")
	if got := readFixtureFile(t, saved); got != agyKeychainToken("alice") {
		t.Fatalf("vault token = %q, want the unwrapped keychain payload", got)
	}
	info, err := os.Stat(f.tokenPath)
	if err != nil {
		t.Fatalf("stat mirror: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("mirror mode = %o, want 600", perm)
	}
	if id := f.vault.ProfileIdentity("agy", "alice"); id != "alice@example.com" {
		t.Fatalf("ProfileIdentity = %q, want the active Google account", id)
	}
}

func TestAgyBackupRefusesWhenNothingIsStored(t *testing.T) {
	f := newAgyKeychainFixture(t)
	writeFixtureFile(t, f.accountsPath, `{"active":"alice@example.com","old":[]}`)

	if HasAuthFiles(f.fileSet) {
		t.Fatal("HasAuthFiles reported a login with no token anywhere")
	}
	err := f.vault.Backup(f.fileSet, "alice")
	if err == nil {
		t.Fatal("Backup succeeded with no token anywhere")
	}
	for _, want := range []string{f.tokenPath, keychain.AgyService, keychain.AgyAccount} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Backup error %q does not mention %q", err, want)
		}
	}
}

func TestAgyRestorePushesTokenToKeychain(t *testing.T) {
	f := newAgyKeychainFixture(t)

	f.storeToken(agyKeychainToken("alice"))
	writeFixtureFile(t, f.accountsPath, `{"active":"alice@example.com","old":[]}`)
	if err := f.vault.Backup(f.fileSet, "alice"); err != nil {
		t.Fatalf("Backup alice: %v", err)
	}
	f.storeToken(agyKeychainToken("bob"))
	writeFixtureFile(t, f.accountsPath, `{"active":"bob@example.com","old":["alice@example.com"]}`)
	if err := f.vault.Backup(f.fileSet, "bob"); err != nil {
		t.Fatalf("Backup bob: %v", err)
	}

	if err := f.vault.Restore(f.fileSet, "alice"); err != nil {
		t.Fatalf("Restore alice: %v", err)
	}
	stored, ok := f.storedToken()
	if !ok {
		t.Fatal("Restore left no keychain item")
	}
	// Stored in the envelope agy itself unwraps: the payload ends in a
	// newline, so go-keyring base64-wraps it.
	want := "go-keyring-base64:" + base64.StdEncoding.EncodeToString([]byte(agyKeychainToken("alice")))
	if stored != want {
		t.Fatalf("keychain holds %q after activating alice, want the enveloped alice token", stored)
	}
	if got := readFixtureFile(t, f.tokenPath); got != agyKeychainToken("alice") {
		t.Fatalf("mirror holds %q after activating alice", got)
	}
	if got := readFixtureFile(t, f.accountsPath); !strings.Contains(got, `"active":"alice@example.com"`) {
		t.Fatalf("google_accounts.json not restored: %s", got)
	}
	if name, _ := f.vault.ActiveProfile(f.fileSet); name != "alice" {
		t.Fatalf("ActiveProfile = %q, want alice", name)
	}
}

func TestAgyRestoreFailsWhenKeychainRefuses(t *testing.T) {
	f := newAgyKeychainFixture(t)
	f.storeToken(agyKeychainToken("alice"))
	if err := f.vault.Backup(f.fileSet, "alice"); err != nil {
		t.Fatalf("Backup: %v", err)
	}
	t.Setenv("CAAM_FAKE_KEYCHAIN_LOCKED", "1")
	if err := f.vault.Restore(f.fileSet, "alice"); err == nil || !strings.Contains(err.Error(), "keychain") {
		t.Fatalf("Restore against a locked keychain = %v, want a keychain error", err)
	}
}

func TestAgyClearAuthFilesRemovesKeychainItem(t *testing.T) {
	f := newAgyKeychainFixture(t)
	f.storeToken(agyKeychainToken("alice"))
	writeFixtureFile(t, f.tokenPath, agyKeychainToken("alice"))

	if err := ClearAuthFiles(f.fileSet); err != nil {
		t.Fatalf("ClearAuthFiles: %v", err)
	}
	if _, ok := f.storedToken(); ok {
		t.Fatal("ClearAuthFiles left the keychain item behind")
	}
}

// TestAgyBridgeIgnoresRelocatedGeminiHome: a GEMINI_HOME outside HOME (a
// shallow lane) belongs to the file it names, not to the login keychain.
func TestAgyBridgeIgnoresRelocatedGeminiHome(t *testing.T) {
	f := newAgyKeychainFixture(t)
	f.storeToken(agyKeychainToken("keychain"))
	t.Setenv("GEMINI_HOME", filepath.Join(t.TempDir(), "lane", ".gemini"))
	fileSet := AntigravityAuthFiles()
	if HasAuthFiles(fileSet) {
		t.Fatal("a relocated GEMINI_HOME with no token file was reported as logged in from the keychain")
	}
}

// TestResnapshotOutgoingSkipsTokenlessClaudeProfile: a profile captured
// without an OAuth credential has no rotating chain to refresh, so the
// switch away from it must not be blocked by Backup's refusal to vault a
// token-less snapshot.
func TestResnapshotOutgoingSkipsTokenlessClaudeProfile(t *testing.T) {
	f := newKeychainFixture(t)
	t.Setenv("CAAM_KEYCHAIN", "0")
	writeFixtureFile(t, f.statePath, keychainState("alice@example.com"))
	// The vault profile holds only the session state, as an older backup or
	// a settings-only login leaves it.
	if err := os.MkdirAll(filepath.Join(f.vaultDir, "claude", "alice"), 0700); err != nil {
		t.Fatal(err)
	}
	writeFixtureFile(t, filepath.Join(f.vaultDir, "claude", "alice", ".claude.json"), keychainState("alice@example.com"))

	if err := f.vault.ResnapshotOutgoing(f.fileSet, "alice", "bob"); err != nil {
		t.Fatalf("ResnapshotOutgoing blocked a switch away from a token-less profile: %v", err)
	}
	// With a live credential present the profile is re-captured as usual.
	writeFixtureFile(t, f.credPath, keychainCreds("at-live"))
	if err := f.vault.ResnapshotOutgoing(f.fileSet, "alice", "bob"); err != nil {
		t.Fatalf("ResnapshotOutgoing: %v", err)
	}
	if got := readFixtureFile(t, filepath.Join(f.vaultDir, "claude", "alice", ".credentials.json")); got != keychainCreds("at-live") {
		t.Fatalf("vault credential after re-capture = %q", got)
	}
}

// TestRestoreRefusesTokenlessClaudeSnapshot: a vault profile that holds
// settings but no credential (captured before the keychain bridge, or from
// a logged-out state) must not "activate": that would install the settings,
// push nothing to the keychain, and report success while the live login
// stays what it was. Restore refuses before touching anything.
func TestRestoreRefusesTokenlessClaudeSnapshot(t *testing.T) {
	f := newKeychainFixture(t)
	live := keychainCreds("at-live")
	f.storeToken(live)

	profileDir := filepath.Join(f.vaultDir, "claude", "alice")
	if err := os.MkdirAll(profileDir, 0700); err != nil {
		t.Fatal(err)
	}
	writeFixtureFile(t, filepath.Join(profileDir, ".claude.json"), keychainState("alice@example.com"))

	err := f.vault.Restore(f.fileSet, "alice")
	if err == nil {
		t.Fatal("Restore installed a profile that holds no credential")
	}
	for _, want := range []string{"no credential", "/login", "caam backup claude alice"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Restore error %q does not mention %q", err, want)
		}
	}
	if got, ok := f.storedToken(); !ok || got != live {
		t.Fatalf("live keychain item changed: ok=%v", ok)
	}
	if fileExists(f.statePath) {
		t.Fatalf("Restore wrote %s before refusing", f.statePath)
	}
}

// TestRestoreAcceptsAPIKeyModeSnapshot: settings.json carrying an API-key
// helper is a credential in its own right, and such a profile still restores.
func TestRestoreAcceptsAPIKeyModeSnapshot(t *testing.T) {
	f := newKeychainFixture(t)
	settingsPath := filepath.Join(filepath.Dir(f.credPath), "settings.json")
	f.fileSet.Files = append(f.fileSet.Files, AuthFileSpec{Tool: "claude", Path: settingsPath, Required: false})

	profileDir := filepath.Join(f.vaultDir, "claude", "alice")
	if err := os.MkdirAll(profileDir, 0700); err != nil {
		t.Fatal(err)
	}
	writeFixtureFile(t, filepath.Join(profileDir, "settings.json"), `{"apiKeyHelper":"/usr/local/bin/key-helper","enabledPlugins":{}}`)

	if err := f.vault.Restore(f.fileSet, "alice"); err != nil {
		t.Fatalf("Restore rejected an API-key-mode profile: %v", err)
	}
	if !fileExists(settingsPath) {
		t.Fatalf("settings.json not restored")
	}
}

// Clearing one tool's credential must not log another tool out: an
// optional file that another tool's set also lists (Antigravity and the
// Gemini CLI share ~/.gemini/oauth_creds.json and google_accounts.json)
// stays; the tool's own required credential goes.
func TestClearAuthFiles_LeavesFilesSharedWithAnotherTool(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GEMINI_HOME", filepath.Join(home, ".gemini"))
	t.Setenv("CAAM_KEYCHAIN", "0")
	agy := AntigravityAuthFiles()
	write := func(path string) {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(`{"synthetic":true}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, spec := range agy.Files {
		write(spec.Path)
	}
	if err := ClearAuthFiles(agy); err != nil {
		t.Fatalf("ClearAuthFiles: %v", err)
	}
	for _, spec := range agy.Files {
		_, err := os.Stat(spec.Path)
		base := filepath.Base(spec.Path)
		switch base {
		case "oauth_creds.json", "google_accounts.json":
			if err != nil {
				t.Errorf("%s is Gemini's too and must survive an Antigravity clear", spec.Path)
			}
		case "antigravity-oauth-token":
			if !os.IsNotExist(err) {
				t.Errorf("%s is Antigravity's own credential and must be cleared (err=%v)", spec.Path, err)
			}
		}
	}
}
