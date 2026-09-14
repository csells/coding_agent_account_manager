// Package authfile manages auth file backup/restore for instant account switching.
//
// The core insight: AI coding tools store OAuth tokens in specific files.
// Instead of logging in/out (slow, requires browser), we can:
//  1. Backup the auth file after logging in once
//  2. Label it with the account name
//  3. Restore it instantly when we need to switch
//
// This enables sub-second account switching for "all you can eat" subscriptions
// like GPT Pro, Claude Max, and Gemini Ultra when hitting usage limits.
package authfile

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/keychain"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/zcodecred"
)

// AuthFileSpec defines where a tool stores its auth credentials.
type AuthFileSpec struct {
	// Tool is the tool identifier (codex, claude, gemini).
	Tool string

	// Path is the absolute path to the auth file.
	Path string

	// Description is a human-readable description.
	Description string

	// Required indicates if this file must exist for auth to work.
	Required bool
}

// AuthFileSet is a collection of auth files that together represent
// a complete authentication state for a tool.
type AuthFileSet struct {
	Tool  string
	Files []AuthFileSpec
	// AllowOptionalOnly permits auth states that rely solely on optional files
	// (e.g., API key or helper-based auth that doesn't create OAuth artifacts).
	AllowOptionalOnly bool
}

// CodexAuthFiles returns the auth files for Codex CLI.
// Codex stores auth in $CODEX_HOME/auth.json (default ~/.codex/auth.json).
func CodexAuthFiles() AuthFileSet {
	home := os.Getenv("CODEX_HOME")
	if home == "" {
		homeDir, _ := os.UserHomeDir()
		home = filepath.Join(homeDir, ".codex")
	}

	return AuthFileSet{
		Tool: "codex",
		Files: []AuthFileSpec{
			{
				Tool:        "codex",
				Path:        filepath.Join(home, "auth.json"),
				Description: "Codex CLI OAuth token (GPT Pro subscription)",
				Required:    true,
			},
		},
	}
}

// ClaudeAuthFiles returns the auth files for Claude Code.
// Claude Code stores OAuth credentials in:
//   - ~/.claude/.credentials.json (primary - contains claudeAiOauth with tokens)
//   - ~/.claude.json (settings file - not auth, but backed up for completeness)
//   - ~/.config/claude-code/auth.json (auth credentials; or $CLAUDE_CONFIG_DIR/auth.json)
//   - ~/.claude/settings.json (user settings)
//   - ~/Library/Application Support/Claude/config.json (macOS: Claude Desktop's
//     encrypted OAuth token cache; only its oauth:tokenCache* fields are tracked)
func ClaudeAuthFiles() AuthFileSet {
	homeDir, _ := os.UserHomeDir()
	claudeConfigDir := os.Getenv("CLAUDE_CONFIG_DIR")
	if claudeConfigDir == "" {
		xdgConfig := os.Getenv("XDG_CONFIG_HOME")
		if xdgConfig == "" {
			xdgConfig = filepath.Join(homeDir, ".config")
		}
		claudeConfigDir = filepath.Join(xdgConfig, "claude-code")
	}

	return AuthFileSet{
		Tool: "claude",
		Files: []AuthFileSpec{
			{
				Tool:        "claude",
				Path:        filepath.Join(homeDir, ".claude", ".credentials.json"),
				Description: "Claude Code OAuth credentials (Claude Max subscription)",
				Required:    true,
			},
			{
				Tool:        "claude",
				Path:        filepath.Join(homeDir, ".claude.json"),
				Description: "Claude Code settings and session state",
				Required:    false, // This is a settings file, not strictly required for auth
			},
			{
				Tool:        "claude",
				Path:        filepath.Join(claudeConfigDir, "auth.json"),
				Description: "Claude Code auth credentials",
				Required:    false,
			},
			{
				Tool:        "claude",
				Path:        filepath.Join(homeDir, ".claude", "settings.json"),
				Description: "Claude Code user settings (apiKeyHelper / API key mode)",
				Required:    false,
			},
			{
				Tool:        "claude",
				Path:        claudeDesktopConfigPath(homeDir),
				Description: "Claude Desktop encrypted OAuth token cache (macOS)",
				Required:    false,
			},
		},
		AllowOptionalOnly: true,
	}
}

// claudeDesktopConfigPath is the macOS Claude Desktop config that holds the
// encrypted OAuth token cache recent Claude Code builds can rehydrate from.
func claudeDesktopConfigPath(homeDir string) string {
	return filepath.Join(homeDir, "Library", "Application Support", "Claude", "config.json")
}

// GeminiAuthFiles returns the auth files for Gemini CLI.
// Gemini CLI stores Google OAuth tokens in ~/.gemini/ directory.
func GeminiAuthFiles() AuthFileSet {
	homeDir, _ := os.UserHomeDir()

	// Check for GEMINI_HOME override
	geminiHome := os.Getenv("GEMINI_HOME")
	if geminiHome == "" {
		geminiHome = filepath.Join(homeDir, ".gemini")
	}

	return AuthFileSet{
		Tool: "gemini",
		Files: []AuthFileSpec{
			{
				Tool:        "gemini",
				Path:        filepath.Join(geminiHome, "settings.json"),
				Description: "Gemini CLI settings with Google OAuth state (Gemini Ultra subscription)",
				Required:    true,
			},
			// Additional auth files that may store tokens
			{
				Tool:        "gemini",
				Path:        filepath.Join(geminiHome, "oauth_creds.json"),
				Description: "Gemini CLI OAuth credentials cache",
				Required:    false,
			},
			{
				Tool:        "gemini",
				Path:        filepath.Join(geminiHome, "google_accounts.json"),
				Description: "Active Google account for the Gemini CLI (google_accounts.json)",
				Required:    false,
			},
			{
				Tool:        "gemini",
				Path:        filepath.Join(geminiHome, ".env"),
				Description: "Gemini API key (.env file)",
				Required:    false,
			},
		},
		AllowOptionalOnly: true,
	}
}

// AntigravityAuthFiles returns the auth files for the Antigravity CLI (agy),
// Google's successor to the legacy Gemini CLI (gmi).
//
// agy is authenticated solely by an on-disk OAuth token at
// ~/.gemini/antigravity-cli/antigravity-oauth-token (this file alone is
// sufficient; it is NOT device-bound). The active Google account email is
// recorded in ~/.gemini/google_accounts.json, and the shared Google OAuth creds
// cache lives at ~/.gemini/oauth_creds.json. The antigravity-cli settings.json
// carries the default model.
//
// Keyring note: agy does NOT use the OS keyring (libsecret) on Linux — the token
// file is the authoritative credential, so caam backs up files only.
//
// Every basename here is unique, so files from the two directories
// (~/.gemini and ~/.gemini/antigravity-cli) never collide in the vault.
func AntigravityAuthFiles() AuthFileSet {
	homeDir, _ := os.UserHomeDir()

	geminiHome := os.Getenv("GEMINI_HOME")
	if geminiHome == "" {
		geminiHome = filepath.Join(homeDir, ".gemini")
	}
	antigravityHome := filepath.Join(geminiHome, "antigravity-cli")

	return AuthFileSet{
		Tool: "agy",
		Files: []AuthFileSpec{
			{
				Tool:        "agy",
				Path:        filepath.Join(antigravityHome, "antigravity-oauth-token"),
				Description: "Antigravity CLI OAuth token (authoritative agy credential)",
				Required:    true,
			},
			{
				Tool:        "agy",
				Path:        filepath.Join(geminiHome, "google_accounts.json"),
				Description: "Active Google account for Antigravity (google_accounts.json)",
				Required:    false,
			},
			{
				Tool:        "agy",
				Path:        filepath.Join(geminiHome, "oauth_creds.json"),
				Description: "Shared Google OAuth credentials cache (oauth_creds.json)",
				Required:    false,
			},
			{
				Tool:        "agy",
				Path:        filepath.Join(antigravityHome, "settings.json"),
				Description: "Antigravity CLI settings (default model / telemetry)",
				Required:    false,
			},
		},
		// The token file is required; AllowOptionalOnly is left false so a backup
		// without the token correctly fails (an account snapshot is meaningless
		// without the authoritative credential).
	}
}

// GrokAuthFiles returns the auth files for xAI's official Grok CLI ("Grok Build").
//
// The CLI stores its login credential in $GROK_HOME/auth.json (default
// ~/.grok/auth.json), written by `grok login`, alongside config.toml in the
// same directory. Both paths and the GROK_HOME override ("Override config
// directory (default: ~/.grok)") are confirmed from the official installer
// (https://x.ai/cli/install.sh) and the CLI's bundled documentation.
//
// Disambiguation: an unaffiliated community CLI (superagent-ai/grok-cli, npm
// grok-dev) also uses ~/.grok/ but stores its state in grok.db and
// user-settings.json. caam deliberately touches ONLY auth.json and
// config.toml — the official Grok Build files — so the two CLIs can coexist
// without caam clobbering community-CLI state.
func GrokAuthFiles() AuthFileSet {
	home := os.Getenv("GROK_HOME")
	if home == "" {
		homeDir, _ := os.UserHomeDir()
		home = filepath.Join(homeDir, ".grok")
	}

	return AuthFileSet{
		Tool: "grok",
		Files: []AuthFileSpec{
			{
				Tool:        "grok",
				Path:        filepath.Join(home, "auth.json"),
				Description: "Grok Build CLI login credential (written by 'grok login')",
				Required:    true,
			},
			{
				Tool:        "grok",
				Path:        filepath.Join(home, "config.toml"),
				Description: "Grok Build CLI configuration",
				Required:    false,
			},
		},
	}
}

// OpenCodeAuthFiles returns the auth files for OpenCode.
//
// Current OpenCode stores its logins in $XDG_DATA_HOME/opencode/opencode.db
// (default ~/.local/share/opencode/opencode.db): the account, account_state,
// control_account and credential tables. That database is bridged rather
// than copied — see opencode.go — and its export is the required credential.
// Older installs wrote $XDG_DATA_HOME/opencode/auth.json instead; it stays
// in the set as an optional file so they keep working.
func OpenCodeAuthFiles() AuthFileSet {
	homeDir, _ := os.UserHomeDir()

	dataHome := os.Getenv("XDG_DATA_HOME")
	if dataHome == "" {
		dataHome = filepath.Join(homeDir, ".local", "share")
	}

	return AuthFileSet{
		Tool: "opencode",
		Files: []AuthFileSpec{
			{
				Tool:        "opencode",
				Path:        filepath.Join(dataHome, "opencode", openCodeStoreFile),
				Description: "OpenCode logins (account and credential tables of opencode.db)",
				Required:    true,
			},
			{
				Tool:        "opencode",
				Path:        filepath.Join(dataHome, "opencode", "auth.json"),
				Description: "OpenCode auth credentials (older installs)",
				Required:    false,
			},
		},
		AllowOptionalOnly: true,
	}
}

// KimiAuthFiles returns the auth files for Kimi Code (Moonshot AI's coding
// CLI). The CLI keeps one plain OAuth token file at
// $KIMI_CODE_HOME/credentials/kimi-code.json (default ~/.kimi-code/...):
// {"access_token","refresh_token","expires_at",...}. It renews the access
// token itself from the refresh token. A logged-out install leaves the file
// behind with empty tokens, which HasAuthFiles and Backup treat as no login.
func KimiAuthFiles() AuthFileSet {
	home := strings.TrimSpace(os.Getenv("KIMI_CODE_HOME"))
	if home == "" {
		homeDir, _ := os.UserHomeDir()
		home = filepath.Join(homeDir, ".kimi-code")
	}
	return AuthFileSet{
		Tool: "kimi",
		Files: []AuthFileSpec{
			{
				Tool:        "kimi",
				Path:        filepath.Join(home, "credentials", "kimi-code.json"),
				Description: "Kimi Code OAuth token (Kimi For Coding subscription)",
				Required:    true,
			},
		},
	}
}

// ZcodeAuthFiles returns the auth files for zcode (Z.ai's coding harness).
// `zcode login` writes one shared, sealed record at
// $ZCODE_DATA_BASE_DIR-or-~/.zcode/v2/credentials.json holding the Z.ai
// access token, the zcode session JWT and the user's profile; every value
// is AES-GCM sealed under a per-user secret, so the file is captured and
// restored verbatim and unsealed only to read identity.
func ZcodeAuthFiles() AuthFileSet {
	return AuthFileSet{
		Tool: "zcode",
		Files: []AuthFileSpec{
			{
				Tool:        "zcode",
				Path:        zcodecred.DefaultPath(),
				Description: "zcode shared Z.AI login (sealed credential record)",
				Required:    true,
			},
		},
	}
}

// CursorAuthFiles returns the auth files for Cursor CLI.
// Cursor stores config in ~/.cursor/ directory.
func CursorAuthFiles() AuthFileSet {
	homeDir, _ := os.UserHomeDir()

	return AuthFileSet{
		Tool: "cursor",
		Files: []AuthFileSpec{
			{
				Tool:        "cursor",
				Path:        filepath.Join(homeDir, ".cursor", "cli-config.json"),
				Description: "Cursor CLI auth (authInfo)",
				Required:    false,
			},
			{
				Tool:        "cursor",
				Path:        filepath.Join(homeDir, ".cursor", "auth.json"),
				Description: "Cursor CLI auth credentials (legacy)",
				Required:    false,
			},
			{
				Tool:        "cursor",
				Path:        filepath.Join(homeDir, ".cursor", "settings.json"),
				Description: "Cursor CLI settings",
				Required:    false,
			},
		},
		AllowOptionalOnly: true,
	}
}

// GetAuthFileSet returns the AuthFileSet for the given provider name.
func GetAuthFileSet(provider string) (AuthFileSet, bool) {
	id := strings.ToLower(provider)
	if canonical, ok := authFileAliases[id]; ok {
		id = canonical
	}
	get, ok := authFileSets[id]
	if !ok {
		return AuthFileSet{}, false
	}
	return get(), true
}

// authFileSets is the file set of every tool caam vaults, by its canonical
// id. It is the one list of tools this package knows.
var authFileSets = map[string]func() AuthFileSet{
	"claude":   ClaudeAuthFiles,
	"codex":    CodexAuthFiles,
	"gemini":   GeminiAuthFiles,
	"agy":      AntigravityAuthFiles,
	"grok":     GrokAuthFiles,
	"opencode": OpenCodeAuthFiles,
	"cursor":   CursorAuthFiles,
	"kimi":     KimiAuthFiles,
	"zcode":    ZcodeAuthFiles,
}

// authFileAliases are the other names GetAuthFileSet accepts for a tool.
var authFileAliases = map[string]string{
	"antigravity": "agy",
	"grok-build":  "grok",
	"oc":          "opencode",
	"cur":         "cursor",
	"kimi-code":   "kimi",
}

// Vault manages stored auth file backups.
type Vault struct {
	basePath string // ~/.local/share/caam/vault
}

const originalProfileName = "_original"

// IsSystemProfile reports whether a profile name is reserved for system-managed
// profiles (created automatically by caam safety features).
//
// Convention: profile names starting with '_' are system profiles.
func IsSystemProfile(name string) bool {
	return strings.HasPrefix(strings.TrimSpace(name), "_")
}

var errProtectedSystemProfile = fmt.Errorf("protected system profile")

// NewVault creates a new vault at the given path.
func NewVault(basePath string) *Vault {
	return &Vault{basePath: basePath}
}

// BasePath returns the on-disk path to the vault root directory.
func (v *Vault) BasePath() string {
	return v.basePath
}

// DefaultVaultPath returns the default vault location.
// Falls back to current directory if home directory cannot be determined.
func DefaultVaultPath() string {
	if caamHome := os.Getenv("CAAM_HOME"); caamHome != "" {
		return filepath.Join(caamHome, "data", "vault")
	}
	if xdgData := os.Getenv("XDG_DATA_HOME"); xdgData != "" {
		return filepath.Join(xdgData, "caam", "vault")
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		// Fallback to current directory - unusual but handles edge cases
		return filepath.Join(".local", "share", "caam", "vault")
	}
	return filepath.Join(homeDir, ".local", "share", "caam", "vault")
}

// ProfilePath returns the path to a profile's backup directory.
// Structure: vault/<tool>/<profile>/
func (v *Vault) ProfilePath(tool, profile string) string {
	return filepath.Join(v.basePath, tool, profile)
}

// BackupPath returns the path where a specific auth file is backed up.
// Structure: vault/<tool>/<profile>/<filename>
func (v *Vault) BackupPath(tool, profile, filename string) string {
	return filepath.Join(v.ProfilePath(tool, profile), filename)
}

// Backup saves the current auth files to the vault.
func (v *Vault) Backup(fileSet AuthFileSet, profile string) error {
	profileDir, err := v.safeProfileDir(fileSet.Tool, profile)
	if err != nil {
		return err
	}

	tool := strings.TrimSpace(fileSet.Tool)
	profile = strings.TrimSpace(profile)

	// System profiles are immutable safety artifacts; never overwrite them.
	if IsSystemProfile(profile) {
		st, err := os.Stat(profileDir)
		if err == nil {
			if st.IsDir() {
				return fmt.Errorf("%w: refusing to overwrite %s/%s", errProtectedSystemProfile, tool, profile)
			}
			return fmt.Errorf("profile path exists and is not a directory: %s", profileDir)
		}
		if err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("stat profile dir: %w", err)
		}
	}

	// On macOS the live Claude and Antigravity credentials are in the login
	// keychain, not on disk. Mirror them out before the walk below, or the
	// snapshot captures settings with no token in them (issue #98). A refused
	// keychain is fatal here: a token-less profile is worse than a failed
	// backup.
	if err := pullKeychain(fileSet); err != nil {
		return err
	}

	// Create profile directory
	if err := os.MkdirAll(profileDir, 0700); err != nil {
		return fmt.Errorf("create profile dir: %w", err)
	}

	backedUp := 0
	requiredFound := false
	optionalFound := false
	var missingRequired []string
	var originalPaths []string
	for _, spec := range fileSet.Files {
		// Claude Desktop config: capture ONLY the oauth:tokenCache* fields, so we
		// never persist (or later clobber) unrelated desktop settings (PR #44).
		if isClaudeDesktopConfig(fileSet.Tool, spec.Path) {
			fields, ok, err := claudeDesktopTokenCache(spec.Path)
			if err != nil {
				return err
			}
			if !ok {
				continue // no token cache present — nothing to back up
			}
			destPath := filepath.Join(profileDir, filepath.Base(spec.Path))
			if err := writeJSONFileAtomic(destPath, fields, 0600); err != nil {
				return fmt.Errorf("backup %s: %w", spec.Path, err)
			}
			backedUp++
			optionalFound = true
			originalPaths = append(originalPaths, spec.Path)
			continue
		}

		// OpenCode database: export only the auth tables, as JSON (see
		// opencode.go); the file itself is also the user's session history.
		if isOpenCodeStore(fileSet.Tool, spec.Path) {
			snap, ok, err := exportOpenCodeAuth(spec.Path)
			if err != nil {
				return fmt.Errorf("backup %s: %w", spec.Path, err)
			}
			if !ok || !snap.hasRows() {
				if spec.Required {
					missingRequired = append(missingRequired, spec.Path)
				}
				continue
			}
			destPath := filepath.Join(profileDir, openCodeVaultFile)
			if err := writeJSONFileAtomic(destPath, snap, 0600); err != nil {
				return fmt.Errorf("backup %s: %w", spec.Path, err)
			}
			backedUp++
			requiredFound = requiredFound || spec.Required
			optionalFound = optionalFound || !spec.Required
			originalPaths = append(originalPaths, spec.Path)
			continue
		}

		if _, err := os.Stat(spec.Path); os.IsNotExist(err) || !fileCarriesLogin(fileSet.Tool, spec.Path) {
			if spec.Required {
				missingRequired = append(missingRequired, spec.Path)
			}
			continue // Skip optional files that don't exist
		}

		// Copy file to vault
		filename := filepath.Base(spec.Path)
		destPath := filepath.Join(profileDir, filename)

		if err := copyFile(spec.Path, destPath); err != nil {
			return fmt.Errorf("backup %s: %w", spec.Path, err)
		}
		backedUp++
		if spec.Required {
			requiredFound = true
		} else {
			optionalFound = true
		}
		originalPaths = append(originalPaths, spec.Path)
	}

	if backedUp == 0 {
		// A required file that exists but holds no login (Kimi's logged-out
		// kimi-code.json, a session-less zcode record) is named as such.
		for _, path := range missingRequired {
			if fileExists(path) {
				return missingRequiredBackupError(fileSet, path)
			}
		}
		return fmt.Errorf("no auth files found to backup for %s; ensure you're logged in first with '%s' or 'caam add %s'", tool, tool, tool)
	}
	if len(missingRequired) > 0 {
		// A snapshot without the required credential is only an account when
		// an optional file carries a credential of its own (API-key mode).
		// Otherwise it is settings with no token in them — exactly the profile
		// that reported success and vaulted nothing, so it is refused loudly.
		optionalOnlyOK := fileSet.AllowOptionalOnly && !requiredFound && optionalFound && optionalFilesCarryAuth(fileSet)
		if !optionalOnlyOK {
			return missingRequiredBackupError(fileSet, missingRequired[0])
		}
	}

	// Write metadata
	metaPath := filepath.Join(profileDir, "meta.json")
	meta := struct {
		Tool          string   `json:"tool"`
		Profile       string   `json:"profile"`
		Description   string   `json:"description,omitempty"` // Free-form notes about profile purpose
		BackedUpAt    string   `json:"backed_up_at"`
		Files         int      `json:"files"`
		Type          string   `json:"type,omitempty"`       // user|system
		CreatedBy     string   `json:"created_by,omitempty"` // user|auto|first-activate
		OriginalPaths []string `json:"original_paths,omitempty"`
		Identity      string   `json:"identity,omitempty"`      // Human-readable account identity (email when known)
		IdentityKeys  []string `json:"identity_keys,omitempty"` // Namespaced identity keys used for matching (issue #73)
	}{
		Tool:          tool,
		Profile:       profile,
		BackedUpAt:    time.Now().Format(time.RFC3339),
		Files:         backedUp,
		Type:          "user",
		CreatedBy:     "user",
		OriginalPaths: originalPaths,
	}
	// Record a rotation-stable account identity next to the snapshot so
	// ActiveProfile can still recognize this profile after the tool rotates
	// its tokens — even if the snapshot's settings file goes missing or
	// unparseable later (issue #73). Claude only: the other tools carry their
	// identity inside the credential file itself.
	if tool == "claude" {
		if keys := claudeLiveIdentityKeys(fileSet); len(keys) > 0 {
			meta.Identity = claudeIdentityLabel(keys)
			meta.IdentityKeys = keys
		}
	}
	if IsSystemProfile(profile) {
		meta.Type = "system"
		meta.CreatedBy = "auto"
		if profile == originalProfileName {
			meta.CreatedBy = "first-activate"
		}
	}
	raw, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("marshal metadata: %w", err)
	}

	// Atomic write: write to temp file, fsync, then rename
	dir := filepath.Dir(metaPath)
	f, err := os.CreateTemp(dir, "meta.json.tmp.*")
	if err != nil {
		return fmt.Errorf("create temp metadata file: %w", err)
	}
	tmpPath := f.Name()
	defer os.Remove(tmpPath)

	if _, err := f.Write(raw); err != nil {
		f.Close()
		return fmt.Errorf("write temp metadata file: %w", err)
	}

	if err := f.Chmod(0600); err != nil {
		f.Close()
		return fmt.Errorf("chmod temp metadata file: %w", err)
	}

	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("sync temp metadata file: %w", err)
	}

	if err := f.Close(); err != nil {
		return fmt.Errorf("close temp metadata file: %w", err)
	}

	if err := os.Rename(tmpPath, metaPath); err != nil {
		return fmt.Errorf("rename metadata file: %w", err)
	}

	return nil
}

// RecordProfileIdentity stores a human-readable account identity (an email)
// in a profile's meta.json, for tools whose credential files carry no
// identity of their own and whose identity a caller resolved elsewhere — the
// Antigravity CLI, whose Google account is only known to Google. It is what
// `caam ls` and `caam status` show, and what ActiveProfile can match on once
// the token has rotated. A profile that is not in the vault is an error.
func (v *Vault) RecordProfileIdentity(tool, profile, identity string) error {
	profileDir, err := v.safeProfileDir(tool, profile)
	if err != nil {
		return err
	}
	metaPath := filepath.Join(profileDir, "meta.json")
	data, err := os.ReadFile(metaPath)
	if err != nil {
		return fmt.Errorf("read profile metadata: %w", err)
	}
	var meta map[string]interface{}
	if err := json.Unmarshal(data, &meta); err != nil {
		return fmt.Errorf("parse profile metadata: %w", err)
	}
	identity = strings.TrimSpace(identity)
	if identity == "" {
		return fmt.Errorf("empty identity for %s/%s", tool, profile)
	}
	meta["identity"] = identity
	return writeJSONFileAtomic(metaPath, meta, 0600)
}

// profileMetaIdentity reads the identity Backup (or RecordProfileIdentity)
// stored in a profile's meta.json, "" when absent.
func profileMetaIdentity(profileDir string) string {
	data, err := os.ReadFile(filepath.Join(profileDir, "meta.json"))
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

// optionalFilesCarryAuth reports whether a file set's optional files hold a
// credential of their own, so a snapshot taken without the required file is
// still an account rather than an empty shell. Only Claude has optional files
// that can carry auth (API-key mode via settings.json, the config-dir
// auth.json, or the desktop token cache); every other AllowOptionalOnly tool
// keeps its existing behaviour.
func optionalFilesCarryAuth(fileSet AuthFileSet) bool {
	if fileSet.Tool != "claude" {
		return true
	}
	for _, spec := range fileSet.Files {
		if spec.Required {
			continue
		}
		switch {
		case isClaudeUserSettings(fileSet.Tool, spec.Path):
			if claudeUserSettingsCarryAuth(spec.Path) {
				return true
			}
		case isClaudeDesktopConfig(fileSet.Tool, spec.Path):
			if _, ok, err := claudeDesktopTokenCache(spec.Path); err == nil && ok {
				return true
			}
		case filepath.Base(spec.Path) == "auth.json":
			if fileExists(spec.Path) {
				return true
			}
		}
	}
	return false
}

// claudeUserSettingsCarryAuth reports whether ~/.claude/settings.json puts
// Claude Code in API-key mode: an apiKeyHelper, or an env block that supplies
// the key or auth token directly.
func claudeUserSettingsCarryAuth(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var root map[string]interface{}
	if err := json.Unmarshal(data, &root); err != nil {
		return false
	}
	if jsonString(root, "apiKeyHelper") != "" {
		return true
	}
	if env, ok := root["env"].(map[string]interface{}); ok {
		for _, key := range []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN"} {
			if jsonString(env, key) != "" {
				return true
			}
		}
	}
	return false
}

// missingRequiredBackupError names what a backup could not find. On a Mac
// the Claude credential is a keychain item before it is a file, so the error
// says which item was looked for and under which account, and how to see the
// lookups themselves.
func missingRequiredBackupError(fileSet AuthFileSet, path string) error {
	if fileSet.Tool == "claude" && claudeKeychainPath(fileSet) != "" && filepath.Base(path) == claudeCredentialsFile {
		return fmt.Errorf("no Claude Code credential to back up: %s is absent and the login keychain holds no %q item for account %q; log in with /login in Claude Code, then back up again (CAAM_DEBUG=1 prints every keychain lookup)",
			path, keychain.ClaudeService, keychain.LoginAccount())
	}
	if fileSet.Tool == "agy" && agyKeychainPath(fileSet) == path {
		return fmt.Errorf("no Antigravity credential to back up: %s is absent and the login keychain holds no %q item for account %q; log in with agy, then back up again (CAAM_DEBUG=1 prints every keychain lookup)",
			path, keychain.AgyService, keychain.AgyAccount)
	}
	if isOpenCodeStore(fileSet.Tool, path) {
		if fileExists(path) {
			return fmt.Errorf("no OpenCode login to back up: %s holds no account or credential rows; log in inside OpenCode (`opencode auth login`, or the Console login), then back up again", path)
		}
		return fmt.Errorf("no OpenCode login to back up: %s does not exist; start OpenCode once and log in, then back up again", path)
	}
	if fileExists(path) && !fileCarriesLogin(fileSet.Tool, path) {
		return fmt.Errorf("no %s credential to back up: %s exists but holds no login (logged out); log in with %s, then back up again", fileSet.Tool, path, fileSet.Tool)
	}
	return fmt.Errorf("required auth file not found: %s", path)
}

// HasOriginalBackup reports whether the system-managed `_original` profile exists
// for the given tool.
func (v *Vault) HasOriginalBackup(tool string) (bool, error) {
	profileDir, err := v.safeProfileDir(tool, originalProfileName)
	if err != nil {
		return false, err
	}
	st, err := os.Stat(profileDir)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("stat original profile dir: %w", err)
	}
	if !st.IsDir() {
		return false, fmt.Errorf("original profile path is not a directory: %s", profileDir)
	}
	return true, nil
}

// BackupCurrent creates a timestamped backup of the current auth state.
// Returns the backup profile name (e.g., "_backup_20251217_143022") if created,
// or empty string if there was nothing to back up.
func (v *Vault) BackupCurrent(fileSet AuthFileSet) (string, error) {
	// Only back up when at least one auth file exists.
	if !HasAuthFiles(fileSet) {
		return "", nil
	}

	// Generate timestamped backup name
	timestamp := time.Now().Format("20060102_150405")
	backupName := "_backup_" + timestamp

	if err := v.Backup(fileSet, backupName); err != nil {
		return "", fmt.Errorf("backup current: %w", err)
	}

	return backupName, nil
}

// ResnapshotOutgoing re-captures the live auth files of the currently-active
// profile back into its own vault directory BEFORE a switch overwrites them.
//
// Rationale (Codex/ChatGPT refresh-token rotation): while a profile is active,
// the tool silently rotates its OAuth tokens in place. The vault snapshot taken
// at backup/login time goes stale the moment the first rotation lands. If we
// switch away (clobbering the live file) without re-snapshotting, the stale
// vault copy now holds an already-consumed refresh_token; the NEXT time that
// profile is restored, presenting it trips the IdP's reuse detection and
// revokes the whole token family.
//
// Guards (all skip silently, returning nil):
//   - empty/missing outgoing profile (unknown live state)
//   - system profiles (_original, _backup_* — immutable)
//   - the target profile we are about to switch TO (would be pointless/racey)
//   - no live auth files present
//
// A failed re-snapshot ABORTS the switch: the vault would otherwise be left
// with a stale copy of the outgoing profile, which is the #19 sequence.
// internal/switcher.Switch enforces this; its Force option is the only
// override, and the caller has to say so.
func (v *Vault) ResnapshotOutgoing(fileSet AuthFileSet, outgoing, target string) error {
	outgoing = strings.TrimSpace(outgoing)
	target = strings.TrimSpace(target)

	if outgoing == "" || outgoing == target {
		return nil
	}
	if IsSystemProfile(outgoing) {
		return nil // immutable safety artifacts; never rewrite
	}
	if !HasAuthFiles(fileSet) {
		return nil // nothing live to capture
	}
	// Claude: a live credentials file that lost its refresh token is the
	// residue of a failed refresh (seen in the #73 field data). Re-snapshotting
	// it would overwrite a possibly still-valid vault copy with a dead one, so
	// leave the vault alone and let the switch proceed.
	if fileSet.Tool == "claude" && claudeLiveCredentialsIncomplete(fileSet) {
		return nil
	}

	// Only re-snapshot if the outgoing profile still actually exists in the
	// vault (don't resurrect a deleted profile).
	profileDir, err := v.safeProfileDir(fileSet.Tool, outgoing)
	if err != nil {
		return err
	}
	if st, err := os.Stat(profileDir); err != nil || !st.IsDir() {
		return nil
	}
	// A Claude profile captured without an OAuth credential (settings-only,
	// or API-key mode) has no rotating chain to go stale; when the live
	// state carries none either there is nothing to refresh, and Backup's
	// refusal of a token-less snapshot must not block the switch.
	if fileSet.Tool == "claude" && !fileExists(claudeFileSetPath(fileSet, claudeCredentialsFile)) &&
		!fileExists(filepath.Join(profileDir, claudeCredentialsFile)) {
		return nil
	}

	return v.Backup(fileSet, outgoing)
}

// CaptureSignedIn files the tool's live credential in the vault: under the
// Active Account's own name when caam knows it (ResnapshotOutgoing's rules,
// so system profiles are never rewritten and a Claude credential that lost
// its refresh token is not written over a good copy), else — an unknown
// credential, or one that matches only an immutable system profile — as a
// fresh _backup_ profile. It returns the name the credential is now under,
// "" when nothing is signed in. This is what has to happen before anything
// replaces the live credential.
func (v *Vault) CaptureSignedIn(fileSet AuthFileSet) (string, error) {
	if !HasAuthFiles(fileSet) {
		return "", nil
	}
	active, _ := v.ActiveProfile(fileSet)
	if active != "" && !IsSystemProfile(active) {
		if err := v.ResnapshotOutgoing(fileSet, active, ""); err != nil {
			return "", err
		}
		return active, nil
	}
	name, err := v.BackupCurrent(fileSet)
	if err != nil {
		return "", err
	}
	return name, nil
}

// RotateAutoBackups removes old auto-backup profiles to stay within the limit.
// Backups are sorted by timestamp (oldest first) and oldest are deleted.
// A maxBackups of 0 means unlimited (no rotation).
func (v *Vault) RotateAutoBackups(tool string, maxBackups int) error {
	if maxBackups <= 0 {
		return nil // Unlimited
	}

	profiles, err := v.List(tool)
	if err != nil {
		return fmt.Errorf("list profiles: %w", err)
	}

	// Filter to auto-backup profiles only
	var backups []string
	for _, p := range profiles {
		if strings.HasPrefix(p, "_backup_") {
			backups = append(backups, p)
		}
	}

	// Already within limit?
	if len(backups) <= maxBackups {
		return nil
	}

	// Sort by name (which includes timestamp, so oldest first)
	// _backup_20251217_143022 sorts lexicographically by date/time
	sort.Strings(backups)

	// Delete oldest until we're within limit
	toDelete := len(backups) - maxBackups
	for i := 0; i < toDelete; i++ {
		if err := v.DeleteForce(tool, backups[i]); err != nil {
			return fmt.Errorf("delete old backup %s: %w", backups[i], err)
		}
	}

	return nil
}

// BackupOriginal creates the system-managed `_original` profile for a tool if
// needed. This is intended to preserve a user's pre-caam auth state.
//
// Behavior:
// - No-op if `_original` already exists
// - No-op if no current auth files exist
// - No-op if current auth already matches an existing vault profile
// - Otherwise backups current auth as `_original`
//
// It returns true if a backup was created.
func (v *Vault) BackupOriginal(fileSet AuthFileSet) (bool, error) {
	exists, err := v.HasOriginalBackup(fileSet.Tool)
	if err != nil {
		return false, err
	}
	if exists {
		return false, nil
	}

	// Only back up when at least one auth file exists.
	if !HasAuthFiles(fileSet) {
		return false, nil
	}

	active, err := v.ActiveProfile(fileSet)
	if err != nil {
		return false, fmt.Errorf("detect active profile: %w", err)
	}
	if active != "" {
		return false, nil
	}

	if err := v.Backup(fileSet, originalProfileName); err != nil {
		return false, err
	}
	return true, nil
}

// MigrateGeminiVaultDir renames oauth_credentials.json to oauth_creds.json in a
// vault profile directory if the old name exists and the new name does not.
// CAAM previously stored "oauth_credentials.json" but Gemini CLI reads "oauth_creds.json".
// This is a no-op if the directory already has the new name or has no OAuth file.
func MigrateGeminiVaultDir(dir string) error {
	oldName := filepath.Join(dir, "oauth_credentials.json")
	newName := filepath.Join(dir, "oauth_creds.json")
	if _, err := os.Stat(oldName); err != nil {
		if os.IsNotExist(err) {
			return nil // old file doesn't exist, nothing to migrate
		}
		return err // permission or I/O error
	}
	if _, err := os.Stat(newName); err == nil {
		// New file already exists; remove legacy file to avoid confusion.
		_ = os.Remove(oldName)
		return nil
	}
	if err := os.Rename(oldName, newName); err != nil {
		// Handle race: another process may have completed the migration.
		if _, statErr := os.Stat(newName); statErr == nil {
			return nil
		}
		return err
	}
	return nil
}

// Restore copies backed-up auth files to their original locations.
func (v *Vault) Restore(fileSet AuthFileSet, profile string) error {
	profileDir, err := v.safeProfileDir(fileSet.Tool, profile)
	if err != nil {
		return err
	}

	if _, err := os.Stat(profileDir); os.IsNotExist(err) {
		return fmt.Errorf("profile %s/%s not found in vault; run 'caam ls %s' to see available profiles", fileSet.Tool, profile, fileSet.Tool)
	}

	// Migrate legacy Gemini OAuth filename in vault.
	if fileSet.Tool == "gemini" {
		if err := MigrateGeminiVaultDir(profileDir); err != nil {
			return fmt.Errorf("vault migration (oauth_credentials.json -> oauth_creds.json): %w", err)
		}
	}

	// Refuse, before touching anything, a profile that holds no credential
	// to install. Restoring such a profile used to copy its settings over,
	// push nothing to the keychain and report "Activated" while the live
	// login stayed exactly what it was — a switch that succeeded on paper.
	// Backup already refuses to write this shape of profile; a copy that
	// predates that rule must not be installable either.
	if !vaultProfileCarriesAuth(fileSet, profileDir) {
		return credentialLessProfileError(fileSet.Tool, profile)
	}

	// Mirror the login keychain onto disk first: on macOS it, not the file, is
	// what the freshness guard below must compare the snapshot against, and a
	// keychain caam cannot read is one it cannot write either — better to stop
	// than to report a switch that did not happen (issue #98).
	if err := pullKeychain(fileSet); err != nil {
		return err
	}

	// Capture the live Claude identity BEFORE any file is overwritten: the
	// freshness guard below must compare against the account that is logged
	// in right now, not against the snapshot's settings once those have been
	// copied over the live ~/.claude.json (issue #73).
	var liveClaudeKeys []string
	if fileSet.Tool == "claude" {
		liveClaudeKeys = claudeLiveIdentityKeys(fileSet)
	}

	restored := 0
	requiredFound := false
	optionalFound := false
	var missingRequired []string
	for _, spec := range fileSet.Files {
		filename := vaultFileName(fileSet.Tool, spec.Path)
		srcPath := filepath.Join(profileDir, filename)

		// Check if backup exists
		if _, err := os.Stat(srcPath); os.IsNotExist(err) {
			if spec.Required {
				missingRequired = append(missingRequired, srcPath)
			}
			continue // Skip optional files
		}

		// OpenCode database: write the exported auth rows back into the
		// live database in one transaction (see opencode.go).
		if isOpenCodeStore(fileSet.Tool, spec.Path) {
			snap, err := readOpenCodeSnapshotFile(srcPath)
			if err != nil {
				return fmt.Errorf("restore %s: %w", spec.Path, err)
			}
			if err := applyOpenCodeAuth(spec.Path, snap); err != nil {
				return fmt.Errorf("restore %s: %w", spec.Path, err)
			}
			restored++
			if spec.Required {
				requiredFound = true
			} else {
				optionalFound = true
			}
			continue
		}

		// Claude Desktop config: MERGE the snapshot's oauth:tokenCache* fields
		// into the live desktop config (replacing any stale cache) while leaving
		// unrelated desktop settings intact, so switching the CAAM profile also
		// swaps the account the desktop cache would otherwise reassert (PR #44).
		if isClaudeDesktopConfig(fileSet.Tool, spec.Path) {
			if err := restoreClaudeDesktopTokenCache(srcPath, spec.Path); err != nil {
				return err
			}
			restored++
			optionalFound = true
			continue
		}

		// Ensure parent directory exists
		if err := os.MkdirAll(filepath.Dir(spec.Path), 0700); err != nil {
			return fmt.Errorf("create parent dir for %s: %w", spec.Path, err)
		}

		// Claude user settings (~/.claude/settings.json): restore the snapshot
		// but carry the LIVE machine's plugin state forward (issue #55). Plugin
		// installs/enablement are machine-level workflow state — the plugin
		// content and marketplaces under ~/.claude/plugins/ are shared across
		// accounts already — while settings.json is swapped per account because
		// it can hold apiKeyHelper/env auth. Without the merge, activating an
		// account whose snapshot predates a plugin install "uninstalls" every
		// plugin until the user switches back.
		if isClaudeUserSettings(fileSet.Tool, spec.Path) {
			if err := restoreClaudeUserSettings(srcPath, spec.Path); err != nil {
				return fmt.Errorf("restore %s: %w", spec.Path, err)
			}
			restored++
			if spec.Required {
				requiredFound = true
			} else {
				optionalFound = true
			}
			continue
		}

		// Claude twin of the Codex freshness guard below (issue #73). Claude
		// Code rotates both tokens in place while a profile is active; if the
		// LIVE credentials belong to the SAME account as this snapshot and are
		// strictly fresher, restoring the snapshot would replace working tokens
		// with an expired access token and an already-consumed refresh token
		// (Anthropic then forces an interactive /login). Keep the live file.
		if fileSet.Tool == "claude" && filename == claudeCredentialsFile &&
			v.claudeLiveIsNewer(liveClaudeKeys, profileDir, spec.Path, srcPath) {
			restored++
			if spec.Required {
				requiredFound = true
			} else {
				optionalFound = true
			}
			continue
		}

		// Freshness guard (Codex/ChatGPT refresh-token rotation safety):
		// If the LIVE auth file is the SAME OpenAI identity as this snapshot but
		// was refreshed more recently, the snapshot's refresh_token has already
		// been rotated out (consumed). Restoring it verbatim would trip the
		// IdP's reuse detection and revoke the whole token family, bricking the
		// account. In that case, leave the live file untouched. Different
		// identity / missing live file / older-or-equal live / unparseable
		// timestamps all fall through to the normal verbatim copy, so genuine
		// cross-account switches and non-codex restores are never blocked.
		if fileSet.Tool == "codex" && codexLiveIsNewer(spec.Path, srcPath) {
			restored++
			if spec.Required {
				requiredFound = true
			} else {
				optionalFound = true
			}
			continue
		}

		// Copy from vault to original location
		if err := copyFile(srcPath, spec.Path); err != nil {
			return fmt.Errorf("restore %s: %w", spec.Path, err)
		}
		restored++
		if spec.Required {
			requiredFound = true
		} else {
			optionalFound = true
		}
	}

	if restored == 0 {
		return fmt.Errorf("no auth files restored for %s/%s", fileSet.Tool, profile)
	}
	if len(missingRequired) > 0 {
		optionalOnlyOK := fileSet.AllowOptionalOnly && !requiredFound && optionalFound
		if !optionalOnlyOK {
			return fmt.Errorf("required backup not found: %s", missingRequired[0])
		}
	}

	// The restored file only becomes the account Claude Code (or agy) uses
	// once it is back in the login keychain (issue #98).
	if err := pushKeychain(fileSet); err != nil {
		return err
	}

	return nil
}

// List returns all profiles stored for a tool.
func (v *Vault) List(tool string) ([]string, error) {
	toolDir, err := v.safeToolDir(tool)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(toolDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var profiles []string
	for _, e := range entries {
		if e.IsDir() {
			profiles = append(profiles, e.Name())
		}
	}
	return profiles, nil
}

// ListAll returns all profiles for all tools.
func (v *Vault) ListAll() (map[string][]string, error) {
	result := make(map[string][]string)

	entries, err := os.ReadDir(v.basePath)
	if err != nil {
		if os.IsNotExist(err) {
			return result, nil
		}
		return nil, err
	}

	for _, e := range entries {
		if e.IsDir() {
			profiles, err := v.List(e.Name())
			if err != nil {
				continue
			}
			result[e.Name()] = profiles
		}
	}

	return result, nil
}

// Delete removes a profile from the vault.
func (v *Vault) Delete(tool, profile string) error {
	if IsSystemProfile(profile) {
		return fmt.Errorf("%w: refusing to delete %s/%s without force", errProtectedSystemProfile, tool, profile)
	}
	return v.DeleteForce(tool, profile)
}

// DeleteForce removes a profile from the vault, including system profiles.
// Prefer Delete unless the caller has an explicit reason to remove protected
// profiles.
func (v *Vault) DeleteForce(tool, profile string) error {
	profileDir, err := v.safeProfileDir(tool, profile)
	if err != nil {
		return err
	}
	return os.RemoveAll(profileDir)
}

// CopyProfile creates a copy of a profile with a new name.
// This is a non-destructive operation: the source profile remains unchanged.
// Returns an error if the source doesn't exist or the destination already exists.
func (v *Vault) CopyProfile(tool, srcProfile, dstProfile string) error {
	srcDir, err := v.safeProfileDir(tool, srcProfile)
	if err != nil {
		return fmt.Errorf("invalid source profile: %w", err)
	}
	dstDir, err := v.safeProfileDir(tool, dstProfile)
	if err != nil {
		return fmt.Errorf("invalid destination profile: %w", err)
	}

	// Verify source exists
	if _, err := os.Stat(srcDir); os.IsNotExist(err) {
		return fmt.Errorf("source profile %s/%s not found", tool, srcProfile)
	}

	// Verify destination doesn't exist
	if _, err := os.Stat(dstDir); err == nil {
		return fmt.Errorf("destination profile %s/%s already exists", tool, dstProfile)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("check destination: %w", err)
	}

	// Create destination directory
	if err := os.MkdirAll(dstDir, 0700); err != nil {
		return fmt.Errorf("create destination dir: %w", err)
	}

	// Copy all files from source to destination
	entries, err := os.ReadDir(srcDir)
	if err != nil {
		os.RemoveAll(dstDir) // Cleanup on failure
		return fmt.Errorf("read source dir: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue // Skip subdirectories
		}

		srcPath := filepath.Join(srcDir, entry.Name())
		dstPath := filepath.Join(dstDir, entry.Name())

		if err := copyFile(srcPath, dstPath); err != nil {
			os.RemoveAll(dstDir) // Cleanup on failure
			return fmt.Errorf("copy %s: %w", entry.Name(), err)
		}
	}

	// Update meta.json with new profile name
	metaPath := filepath.Join(dstDir, "meta.json")
	if _, err := os.Stat(metaPath); err == nil {
		// Read and update meta.json
		data, err := os.ReadFile(metaPath)
		if err == nil {
			var meta map[string]interface{}
			if json.Unmarshal(data, &meta) == nil {
				meta["profile"] = dstProfile
				meta["copied_from"] = srcProfile
				meta["copied_at"] = time.Now().Format(time.RFC3339)
				if updated, err := json.MarshalIndent(meta, "", "  "); err == nil {
					// Atomic write for meta.json
					tmpPath := metaPath + ".tmp"
					if err := os.WriteFile(tmpPath, updated, 0600); err == nil {
						os.Rename(tmpPath, metaPath)
					}
				}
			}
		}
	}

	return nil
}

// ActiveProfile returns which profile is currently active (if any).
// It compares the current auth files with vault backups using stable identity
// hashing. For tools like Claude and Codex, only identity-bearing fields are
// hashed so that volatile metadata (e.g., changelogLastFetched, numStartups)
// does not break profile detection.
func (v *Vault) ActiveProfile(fileSet AuthFileSet) (string, error) {
	// Best-effort: on macOS the live token is in the keychain, so without the
	// mirror the hash comparison below has nothing to compare (issue #98).
	// A refused keychain leaves detection where it was before the bridge.
	_ = pullKeychain(fileSet)

	profiles, err := v.List(fileSet.Tool)
	if err != nil {
		return "", err
	}

	// Hash the current auth files using stable identity extraction.
	// Prefer required files for matching; optional files can change frequently
	// (e.g., settings/session files) and should not break profile detection.
	currentHashes := make(map[string]string)
	optionalHashes := make(map[string]string)
	requiredFound := false
	for _, spec := range fileSet.Files {
		// A Claude Desktop config with no token cache carries no identity; skip it
		// so unrelated desktop settings never drive profile detection (PR #44).
		if isClaudeDesktopConfig(fileSet.Tool, spec.Path) {
			if _, ok, err := claudeDesktopTokenCache(spec.Path); err != nil || !ok {
				continue
			}
		}
		if _, err := os.Stat(spec.Path); os.IsNotExist(err) {
			continue
		}
		// An OpenCode database with no login carries no identity.
		if isOpenCodeStore(fileSet.Tool, spec.Path) && !openCodeStoreHasAuth(spec.Path) {
			continue
		}
		hash, err := stableFileHash(fileSet.Tool, spec.Path)
		if err != nil {
			continue
		}
		base := vaultFileName(fileSet.Tool, spec.Path)
		if spec.Required {
			requiredFound = true
			currentHashes[base] = hash
			continue
		}
		optionalHashes[base] = hash
	}

	if !requiredFound {
		if fileSet.AllowOptionalOnly {
			currentHashes = optionalHashes
		}
	}

	if len(currentHashes) == 0 {
		return "", nil // No relevant auth files present
	}

	// Compare with each profile.
	// Prefer user-named profiles over system profiles (_backup_*, _original,
	// _auto_backup_*). System profiles can share the same identity as a named
	// profile (same account re-authenticated), and because they sort
	// alphabetically before most user names (underscore < lowercase letters),
	// they would otherwise shadow the intended named profile.
	var systemMatch string
	for _, profile := range profiles {
		profileDir := v.ProfilePath(fileSet.Tool, profile)
		matches := true

		for filename, currentHash := range currentHashes {
			backupPath := filepath.Join(profileDir, filename)
			backupHash, err := stableFileHash(fileSet.Tool, backupPath)
			if err != nil {
				matches = false
				break
			}
			if currentHash != backupHash {
				matches = false
				break
			}
		}

		if matches {
			if !IsSystemProfile(profile) {
				return profile, nil // Prefer user-named profiles
			}
			if systemMatch == "" {
				systemMatch = profile // Remember first system match as fallback
			}
		}
	}

	if systemMatch != "" {
		return systemMatch, nil // Fall back to a system profile that matched byte-for-byte
	}

	// Claude rotates both OAuth tokens in place while a profile is active, so
	// the token hash above stops matching the profile's own snapshot after the
	// first refresh. Fall back to the account identity carried by
	// ~/.claude.json (and recorded in meta.json at backup time) — issue #73.
	if fileSet.Tool == "claude" {
		return v.claudeActiveProfileByIdentity(fileSet, profiles), nil
	}
	// Google rotates the Antigravity access token hourly, so the token file
	// stops matching its own snapshot within the hour; the active Google
	// account recorded beside it does not change.
	if fileSet.Tool == "agy" {
		return v.agyActiveProfileByIdentity(fileSet, profiles), nil
	}

	return "", nil
}

// HasAuthFiles checks if the tool currently has auth files present.
func HasAuthFiles(fileSet AuthFileSet) bool {
	// Best-effort mirror: a macOS Claude or Antigravity login lives in the
	// keychain, and reporting "not logged in" for it would send callers down
	// the login path (issue #98).
	_ = pullKeychain(fileSet)

	optionalFound := false
	for _, spec := range fileSet.Files {
		// The Claude Desktop config only counts as auth when it holds a token
		// cache (the file also exists for token-less desktop installs).
		if isClaudeDesktopConfig(fileSet.Tool, spec.Path) {
			if _, ok, err := claudeDesktopTokenCache(spec.Path); err == nil && ok {
				optionalFound = true
			}
			continue
		}
		if isOpenCodeStore(fileSet.Tool, spec.Path) {
			if openCodeStoreHasAuth(spec.Path) {
				if spec.Required {
					return true
				}
				optionalFound = true
			}
			continue
		}
		if _, err := os.Stat(spec.Path); err == nil && fileCarriesLogin(fileSet.Tool, spec.Path) {
			if spec.Required {
				return true
			}
			optionalFound = true
		}
	}
	if fileSet.AllowOptionalOnly && optionalFound {
		return true
	}
	return false
}

// fileCarriesLogin reports whether an auth file that exists actually holds a
// login. Most tools delete their file on logout; Kimi Code leaves
// kimi-code.json behind with empty tokens, and a zcode record can hold no
// session, so for those an existing file is checked rather than trusted.
func fileCarriesLogin(tool, path string) bool {
	switch tool {
	case "kimi":
		data, err := os.ReadFile(path)
		if err != nil {
			return false
		}
		var creds struct {
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
		}
		if err := json.Unmarshal(data, &creds); err != nil {
			return true // not our shape; leave it to the caller as before
		}
		return strings.TrimSpace(creds.AccessToken) != "" || strings.TrimSpace(creds.RefreshToken) != ""
	case "zcode":
		rec, err := zcodecred.ReadRecord(path)
		if err != nil {
			// Unreadable (another user's secret) is still a file worth
			// capturing verbatim; only a readable, session-less record is
			// a logout.
			return !errors.Is(err, zcodecred.ErrNoRecord)
		}
		return rec.LoggedIn()
	}
	return true
}

// ClearAuthFiles removes all auth files for a tool (logout).
func ClearAuthFiles(fileSet AuthFileSet) error {
	for _, spec := range fileSet.Files {
		// For the Claude Desktop config, scrub only the oauth:tokenCache* keys so
		// logout does not destroy the user's unrelated desktop settings (PR #44).
		if isClaudeDesktopConfig(fileSet.Tool, spec.Path) {
			if err := scrubClaudeDesktopTokenCache(spec.Path); err != nil {
				return err
			}
			continue
		}
		// The OpenCode database is also the user's session history: empty
		// its auth tables, never remove the file (see opencode.go).
		if isOpenCodeStore(fileSet.Tool, spec.Path) {
			if err := clearOpenCodeAuth(spec.Path); err != nil {
				return err
			}
			continue
		}
		// An optional file another tool's set also lists is that tool's
		// login too (Antigravity and the Gemini CLI share ~/.gemini's OAuth
		// cache): clearing this tool must not log the other one out.
		if !spec.Required && sharedWithAnotherTool(fileSet.Tool, spec.Path) {
			continue
		}
		if err := os.Remove(spec.Path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove %s: %w", spec.Path, err)
		}
	}

	// Removing the mirror is not a logout while the keychain still holds the
	// token Claude Code prefers (issue #98).
	return clearKeychain(fileSet)
}

// sharedWithAnotherTool reports whether another tool's file set lists path.
func sharedWithAnotherTool(tool, path string) bool {
	clean := filepath.Clean(path)
	for other, get := range authFileSets {
		if other == tool {
			continue
		}
		for _, spec := range get().Files {
			if filepath.Clean(spec.Path) == clean {
				return true
			}
		}
	}
	return false
}

// --- Claude Desktop OAuth token cache (macOS) -------------------------------
//
// Recent Claude Code builds on macOS can refresh ~/.claude.json from Claude
// Desktop's encrypted OAuth token cache at
// ~/Library/Application Support/Claude/config.json. If caam swaps only the
// dotfiles, that cache silently reasserts the previous account on the next
// launch (reported in PR #44). caam therefore tracks ONLY the oauth cache
// fields: it captures them on backup, merges them back on restore, and scrubs
// only them on clear — never touching the unrelated Claude Desktop settings
// stored in the same file. The values are opaque encrypted blobs; caam moves
// them verbatim, which is valid for the same-machine backup/restore it performs.
const (
	claudeDesktopTokenKey   = "oauth:tokenCache"
	claudeDesktopTokenKeyV2 = "oauth:tokenCacheV2"
)

var claudeDesktopTokenKeys = []string{claudeDesktopTokenKey, claudeDesktopTokenKeyV2}

// --- Claude Code user settings (~/.claude/settings.json) ---------------------
//
// settings.json is swapped per account because it can carry identity/auth
// (apiKeyHelper, env with ANTHROPIC_API_KEY). But it ALSO carries plugin
// enablement (enabledPlugins), which is machine-level workflow state: the
// plugin content, marketplaces, and install records under ~/.claude/plugins/
// are shared across accounts (caam never touches them), so an account swap
// that reverts enabledPlugins makes installed plugins vanish from /plugin
// while their marketplaces still show — exactly the asymmetry in issue #55.
// On restore, the LIVE machine's value of each shared key wins (including its
// absence), so plugin state persists across `caam activate` like the shared
// plugin content dir does.

// claudeUserSettingsSharedKeys are top-level settings.json keys that describe
// machine-level workflow state (shared across accounts) rather than
// per-account identity. The live values of these keys survive a restore.
var claudeUserSettingsSharedKeys = []string{"enabledPlugins"}

// isClaudeUserSettings reports whether spec.Path is Claude Code's user
// settings file (~/.claude/settings.json), which needs key-scoped merge
// handling on restore rather than a whole-file copy.
func isClaudeUserSettings(tool, path string) bool {
	return tool == "claude" &&
		filepath.Base(path) == "settings.json" &&
		filepath.Base(filepath.Dir(path)) == ".claude"
}

// restoreClaudeUserSettings writes the vault snapshot to livePath while
// preserving the live file's claudeUserSettingsSharedKeys (present value or
// absence). Falls back to a verbatim copy whenever either side is missing or
// not a JSON object — there is then either nothing to preserve or nothing safe
// to merge into.
func restoreClaudeUserSettings(vaultPath, livePath string) error {
	vaultRaw, err := os.ReadFile(vaultPath)
	if err != nil {
		return fmt.Errorf("read snapshot %s: %w", vaultPath, err)
	}
	var vaultObj map[string]interface{}
	if err := json.Unmarshal(vaultRaw, &vaultObj); err != nil || vaultObj == nil {
		return copyFile(vaultPath, livePath) // snapshot not a JSON object: restore verbatim
	}

	liveRaw, err := os.ReadFile(livePath)
	if err != nil {
		if os.IsNotExist(err) {
			return copyFile(vaultPath, livePath) // no live state to preserve
		}
		return fmt.Errorf("read live %s: %w", livePath, err)
	}
	var liveObj map[string]interface{}
	if err := json.Unmarshal(liveRaw, &liveObj); err != nil || liveObj == nil {
		return copyFile(vaultPath, livePath) // live file unparseable: restore verbatim
	}

	for _, key := range claudeUserSettingsSharedKeys {
		if v, ok := liveObj[key]; ok {
			vaultObj[key] = v
		} else {
			delete(vaultObj, key)
		}
	}
	return writeJSONFileAtomic(livePath, vaultObj, 0600)
}

// isClaudeDesktopConfig reports whether spec.Path is the macOS Claude Desktop
// config.json, which needs field-scoped handling rather than whole-file copy.
func isClaudeDesktopConfig(tool, path string) bool {
	return tool == "claude" &&
		filepath.Base(path) == "config.json" &&
		strings.Contains(filepath.ToSlash(path), "/Library/Application Support/Claude/")
}

// claudeDesktopTokenCache reads path and returns just its oauth:tokenCache*
// fields. ok is false when the file is absent or carries no token cache.
func claudeDesktopTokenCache(path string) (fields map[string]interface{}, ok bool, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("read Claude desktop config: %w", err)
	}
	var root map[string]interface{}
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, false, fmt.Errorf("parse Claude desktop config %s: %w", path, err)
	}
	fields = map[string]interface{}{}
	for _, k := range claudeDesktopTokenKeys {
		if v, exists := root[k]; exists {
			fields[k] = v
		}
	}
	return fields, len(fields) > 0, nil
}

// restoreClaudeDesktopTokenCache merges the token-cache fields captured in the
// vault snapshot (vaultPath) into the live desktop config (livePath), replacing
// any stale cache and preserving every other setting. The live file is created
// if it does not yet exist.
func restoreClaudeDesktopTokenCache(vaultPath, livePath string) error {
	fields, ok, err := claudeDesktopTokenCache(vaultPath)
	if err != nil {
		return err
	}
	if !ok {
		return nil // snapshot has nothing to restore
	}
	live := map[string]interface{}{}
	if data, rerr := os.ReadFile(livePath); rerr == nil {
		if uerr := json.Unmarshal(data, &live); uerr != nil {
			return fmt.Errorf("parse live Claude desktop config %s: %w", livePath, uerr)
		}
	} else if !os.IsNotExist(rerr) {
		return fmt.Errorf("read live Claude desktop config: %w", rerr)
	}
	// Drop any stale cache, then apply the snapshot's fields.
	for _, k := range claudeDesktopTokenKeys {
		delete(live, k)
	}
	for k, v := range fields {
		live[k] = v
	}
	if err := os.MkdirAll(filepath.Dir(livePath), 0700); err != nil {
		return fmt.Errorf("create Claude desktop config dir: %w", err)
	}
	if err := writeJSONFileAtomic(livePath, live, 0600); err != nil {
		return fmt.Errorf("write Claude desktop config: %w", err)
	}
	return nil
}

// scrubClaudeDesktopTokenCache deletes only the oauth:tokenCache* keys from the
// live desktop config, leaving all other settings intact. A missing file or a
// file with no token cache is a no-op.
func scrubClaudeDesktopTokenCache(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read Claude desktop config: %w", err)
	}
	var root map[string]interface{}
	if err := json.Unmarshal(data, &root); err != nil {
		return fmt.Errorf("parse Claude desktop config %s: %w", path, err)
	}
	changed := false
	for _, k := range claudeDesktopTokenKeys {
		if _, ok := root[k]; ok {
			delete(root, k)
			changed = true
		}
	}
	if !changed {
		return nil
	}
	if err := writeJSONFileAtomic(path, root, 0600); err != nil {
		return fmt.Errorf("write Claude desktop config: %w", err)
	}
	return nil
}

// hashClaudeDesktopConfig hashes ONLY the oauth:tokenCache* fields, so unrelated
// desktop settings never perturb active-profile detection.
func hashClaudeDesktopConfig(path string) (string, error) {
	fields, ok, err := claudeDesktopTokenCache(path)
	if err != nil {
		return "", err
	}
	if !ok {
		h := sha256.New()
		h.Write([]byte("claude:desktop:no-token"))
		return hex.EncodeToString(h.Sum(nil)), nil
	}
	canonical, err := json.Marshal(fields)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	h.Write([]byte("claude:desktop:"))
	h.Write(canonical)
	return hex.EncodeToString(h.Sum(nil)), nil
}

// writeJSONFileAtomic marshals v (indented) and writes it to path atomically.
func writeJSONFileAtomic(path string, v interface{}, perm os.FileMode) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, filepath.Base(path)+".tmp.*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Chmod(perm); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Helper functions

func copyFile(src, dst string) error {
	// Ensure parent directory exists
	dir := filepath.Dir(dst)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}

	srcFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	// Create temp file for atomic write using CreateTemp to avoid races
	// Pattern: filename.tmp.RANDOM
	dstFile, err := os.CreateTemp(dir, filepath.Base(dst)+".tmp.*")
	if err != nil {
		return err
	}
	tmpPath := dstFile.Name()

	// Ensure cleanup of temp file if something goes wrong.
	// If rename succeeds, this removal will fail (which is fine).
	defer os.Remove(tmpPath)

	if _, err := io.Copy(dstFile, srcFile); err != nil {
		dstFile.Close()
		return err
	}

	// Enforce 0600 permissions for all auth files
	if err := dstFile.Chmod(0600); err != nil {
		dstFile.Close()
		return err
	}

	if err := dstFile.Sync(); err != nil {
		dstFile.Close()
		return err
	}

	if err := dstFile.Close(); err != nil {
		return err
	}

	// Atomic rename
	return os.Rename(tmpPath, dst)
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}

// hashBytes returns a SHA-256 hex digest of raw bytes. Used as a fallback
// when identity extraction fails but we already have the file data in memory,
// avoiding a second disk read (TOCTOU race) that hashFile would require.
func hashBytes(data []byte) string {
	h := sha256.New()
	h.Write(data)
	return hex.EncodeToString(h.Sum(nil))
}

// stableFileHash returns a hash of only the identity-bearing fields in an auth
// file, ignoring volatile metadata that tools write after activation. This
// prevents profile detection from breaking when tools modify non-auth fields.
//
// Falls back to whole-file hashing if identity extraction fails or is not
// implemented for the given tool.
func stableFileHash(tool, path string) (string, error) {
	switch tool {
	case "claude":
		return stableClaudeHash(path)
	case "codex":
		return stableCodexHash(path)
	case "agy":
		return stableAgyHash(path)
	case "kimi":
		return stableKimiHash(path)
	case "zcode":
		return stableZcodeHash(path)
	case "opencode":
		switch filepath.Base(path) {
		case openCodeStoreFile:
			return hashOpenCodeStore(path)
		case openCodeVaultFile:
			return hashOpenCodeVaultFile(path)
		}
		return hashFile(path)
	default:
		return hashFile(path)
	}
}

// stableKimiHash hashes a Kimi Code token file by the account it belongs
// to rather than by the tokens, which the CLI rotates in place: the JWT
// subject of the access token when it is a JWT, else the refresh token
// (which outlives the access token), else the whole file.
func stableKimiHash(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	var creds struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.Unmarshal(data, &creds); err != nil {
		return hashBytes(data), nil
	}
	if sub := jwtSubject(creds.AccessToken); sub != "" {
		return hashLabeled("kimi:subject:", sub), nil
	}
	if creds.RefreshToken != "" {
		return hashLabeled("kimi:refresh-token:", creds.RefreshToken), nil
	}
	return hashBytes(data), nil
}

// stableZcodeHash hashes a zcode credential record by the signed-in user.
// zcode re-seals every value with a fresh IV whenever it rewrites the file,
// so the bytes change without the account changing; the user id (or email)
// in the sealed profile does not. A record that cannot be unsealed hashes
// whole.
func stableZcodeHash(path string) (string, error) {
	rec, err := zcodecred.ReadRecord(path)
	if err != nil {
		if errors.Is(err, zcodecred.ErrNoRecord) {
			return "", err
		}
		return hashFile(path)
	}
	if rec.UserInfo != nil {
		if rec.UserInfo.UserID != "" {
			return hashLabeled("zcode:user-id:", rec.UserInfo.UserID), nil
		}
		if rec.UserInfo.Email != "" {
			return hashLabeled("zcode:email:", rec.UserInfo.Email), nil
		}
	}
	if sub := jwtSubject(rec.AccessToken); sub != "" {
		return hashLabeled("zcode:subject:", sub), nil
	}
	return hashFile(path)
}

// hashLabeled hashes a stable identity value under a namespace label.
func hashLabeled(label, value string) string {
	h := sha256.New()
	h.Write([]byte(label))
	h.Write([]byte(value))
	return hex.EncodeToString(h.Sum(nil))
}

// jwtSubject returns the sub (or user_id) claim of a JWT, "" when the token
// is not a JWT or carries neither.
func jwtSubject(token string) string {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return ""
	}
	payload, err := decodeBase64Segment(parts[1])
	if err != nil {
		return ""
	}
	var claims map[string]interface{}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return ""
	}
	for _, key := range []string{"sub", "user_id", "uid"} {
		if v := jsonString(claims, key); v != "" {
			return v
		}
	}
	return ""
}

// stableAgyHash hashes the Antigravity token file by its refresh token
// alone. Google renews the access token (and its expiry) every hour while
// the same login stays in place, so a whole-file hash stops matching the
// profile's own snapshot within the hour; the refresh token only changes
// with a new login, which is a new account or a new grant either way. A file
// without one hashes whole, as before.
func stableAgyHash(path string) (string, error) {
	if filepath.Base(path) != agyTokenFile {
		return hashFile(path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	var root struct {
		Token struct {
			RefreshToken string `json:"refresh_token"`
		} `json:"token"`
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.Unmarshal(data, &root); err != nil {
		return hashBytes(data), nil
	}
	refresh := root.Token.RefreshToken
	if refresh == "" {
		refresh = root.RefreshToken
	}
	if refresh == "" {
		return hashBytes(data), nil
	}
	return hashLabeled("agy:refresh-token:", refresh), nil
}

// stableClaudeHash extracts identity-bearing fields from Claude auth files and
// hashes only those fields. This handles two file types:
//
//   - .credentials.json: contains claudeAiOauth with accessToken and
//     refreshToken (the actual auth identity)
//   - .claude.json: settings file with oauthAccount (identity) mixed with
//     volatile fields like changelogLastFetched, numStartups, tipsHistory
//
// For .credentials.json, we hash the accessToken and refreshToken.
// For .claude.json, we hash the oauthAccount field only.
// For settings.json, we hash only the auth-bearing fields (apiKeyHelper/env),
// since the rest (enabledPlugins, hooks, UI prefs) is workflow state that
// drifts freely — and the enabledPlugins merge on restore (issue #55) makes
// the live file intentionally diverge from its snapshot.
// For other files (auth.json), we fall back to whole-file hash.
func stableClaudeHash(path string) (string, error) {
	base := filepath.Base(path)

	switch base {
	case ".credentials.json":
		return hashClaudeCredentials(path)
	case ".claude.json":
		return hashClaudeSettings(path)
	case "settings.json":
		return hashClaudeUserSettings(path)
	case "config.json":
		// The Claude Desktop config.json: hash only its oauth:tokenCache* fields.
		return hashClaudeDesktopConfig(path)
	default:
		return hashFile(path)
	}
}

// hashClaudeCredentials hashes the accessToken and refreshToken from
// claudeAiOauth in Claude's .credentials.json. Volatile fields like expiresAt
// are excluded. Note that the tokens themselves are rotated in place by
// Claude Code every few hours of use, so this hash identifies a particular
// token generation, not the account: ActiveProfile treats it as the
// exact-snapshot match and falls back to the account identity carried by
// ~/.claude.json when it no longer matches (issue #73).
func hashClaudeCredentials(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}

	var root map[string]interface{}
	if err := json.Unmarshal(data, &root); err != nil {
		// Not valid JSON; fall back to hashing the bytes we already read
		return hashBytes(data), nil
	}

	oauth, ok := root["claudeAiOauth"].(map[string]interface{})
	if !ok {
		// No claudeAiOauth section; fall back to hashing the bytes we already read
		return hashBytes(data), nil
	}

	// Extract stable identity fields: accessToken and refreshToken uniquely
	// identify the authenticated session/account.
	identityFields := map[string]interface{}{}
	for _, key := range []string{"accessToken", "refreshToken"} {
		if v, exists := oauth[key]; exists {
			identityFields[key] = v
		}
	}

	if len(identityFields) == 0 {
		return hashBytes(data), nil
	}

	// Deterministic JSON serialization for hashing
	canonical, err := json.Marshal(identityFields)
	if err != nil {
		return hashBytes(data), nil
	}

	h := sha256.New()
	h.Write([]byte("claude:credentials:"))
	h.Write(canonical)
	return hex.EncodeToString(h.Sum(nil)), nil
}

// hashClaudeSettings hashes only the identity-bearing oauthAccount field from
// Claude's .claude.json settings file. This file contains many volatile fields
// (changelogLastFetched, numStartups, tipsHistory, etc.) that change
// frequently and would break profile detection if included in the hash.
func hashClaudeSettings(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}

	var root map[string]interface{}
	if err := json.Unmarshal(data, &root); err != nil {
		return hashBytes(data), nil
	}

	// oauthAccount is the identity-bearing field in .claude.json.
	// All other top-level fields are volatile session/UI state.
	identityFields := map[string]interface{}{}
	for _, key := range []string{"oauthAccount", "userID"} {
		if v, exists := root[key]; exists {
			identityFields[key] = v
		}
	}

	if len(identityFields) == 0 {
		// No identity fields found; the file is purely volatile settings.
		// Return a fixed sentinel hash so all settings-only files match,
		// preventing settings drift from breaking profile detection.
		h := sha256.New()
		h.Write([]byte("claude:settings:no-identity"))
		return hex.EncodeToString(h.Sum(nil)), nil
	}

	canonical, err := json.Marshal(identityFields)
	if err != nil {
		return hashBytes(data), nil
	}

	h := sha256.New()
	h.Write([]byte("claude:settings:"))
	h.Write(canonical)
	return hex.EncodeToString(h.Sum(nil)), nil
}

// hashClaudeUserSettings hashes only the auth-bearing fields of Claude's
// ~/.claude/settings.json (apiKeyHelper and env — the API-key-mode identity).
// Everything else in the file (enabledPlugins, hooks, permissions, UI prefs)
// is volatile workflow state; hashing it whole-file made profile detection
// break on any settings tweak, and would ALWAYS break after the restore-time
// enabledPlugins merge (issue #55).
func hashClaudeUserSettings(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}

	var root map[string]interface{}
	if err := json.Unmarshal(data, &root); err != nil {
		return hashBytes(data), nil
	}

	identityFields := map[string]interface{}{}
	for _, key := range []string{"apiKeyHelper", "env"} {
		if v, exists := root[key]; exists {
			identityFields[key] = v
		}
	}

	if len(identityFields) == 0 {
		// No auth-bearing fields: purely workflow settings. Fixed sentinel so
		// settings drift never breaks profile detection (matches the
		// .claude.json no-identity convention above).
		h := sha256.New()
		h.Write([]byte("claude:user-settings:no-identity"))
		return hex.EncodeToString(h.Sum(nil)), nil
	}

	canonical, err := json.Marshal(identityFields)
	if err != nil {
		return hashBytes(data), nil
	}

	h := sha256.New()
	h.Write([]byte("claude:user-settings:"))
	h.Write(canonical)
	return hex.EncodeToString(h.Sum(nil)), nil
}

// stableCodexHash extracts a stable identity from Codex auth files by parsing
// JWT tokens and hashing the identity claims (email, account_id, organization).
// This solves the dedup problem where multiple named profiles with different
// token strings (due to refresh) actually represent the same OpenAI account.
//
// Falls back to whole-file hash if JWT parsing fails.
func stableCodexHash(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}

	var auth map[string]interface{}
	if err := json.Unmarshal(data, &auth); err != nil {
		return hashBytes(data), nil
	}

	// Try to extract stable identity from JWT tokens.
	// Codex stores tokens in various fields; check all candidates.
	identity := extractCodexIdentity(auth)
	if identity != "" {
		h := sha256.New()
		h.Write([]byte("codex:identity:"))
		h.Write([]byte(identity))
		return hex.EncodeToString(h.Sum(nil)), nil
	}

	// JWT parsing failed; fall back to hashing bytes we already read
	return hashBytes(data), nil
}

// extractCodexIdentity extracts a stable identity string from Codex auth data
// by decoding JWT tokens and extracting email/account claims. Returns empty
// string if no identity can be determined.
func extractCodexIdentity(auth map[string]interface{}) string {
	// Ordered by preference: id_token has richer claims than access_token
	tokenFields := []string{"id_token", "idToken"}
	nestedTokenFields := []string{"id_token", "idToken", "access_token", "accessToken"}

	// Check top-level token fields
	for _, field := range tokenFields {
		if token := jsonString(auth, field); token != "" {
			if id := identityFromJWT(token); id != "" {
				return id
			}
		}
	}

	// Check nested tokens object
	if tokens, ok := auth["tokens"].(map[string]interface{}); ok {
		for _, field := range nestedTokenFields {
			if token := jsonString(tokens, field); token != "" {
				if id := identityFromJWT(token); id != "" {
					return id
				}
			}
		}
	}

	// Check top-level access tokens as last resort
	for _, field := range []string{"access_token", "accessToken", "token"} {
		if token := jsonString(auth, field); token != "" {
			if id := identityFromJWT(token); id != "" {
				return id
			}
		}
	}

	return ""
}

// identityFromJWT decodes a JWT token (without signature verification) and
// extracts a stable identity string from its claims. Returns empty string if
// the token is not a valid JWT or contains no identity claims.
func identityFromJWT(token string) string {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[1] == "" {
		return ""
	}

	// Decode the payload segment
	payload, err := decodeBase64Segment(parts[1])
	if err != nil {
		return ""
	}

	var claims map[string]interface{}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return ""
	}

	// Build stable identity from claims, checking known namespaces too.
	// OpenAI/Codex tokens nest some claims under "https://api.openai.com/auth".
	claimMaps := []map[string]interface{}{claims}
	for _, ns := range []string{"https://api.openai.com/auth", "https://api.openai.com/profile"} {
		if nested, ok := claims[ns].(map[string]interface{}); ok {
			claimMaps = append(claimMaps, nested)
		}
	}

	var email, accountID, org string
	for _, m := range claimMaps {
		if email == "" {
			for _, key := range []string{"email", "preferred_username", "upn"} {
				if v := jsonString(m, key); v != "" {
					email = v
					break
				}
			}
		}
		if accountID == "" {
			for _, key := range []string{"sub", "account_id", "accountId", "user_id", "userId"} {
				if v := jsonString(m, key); v != "" {
					accountID = v
					break
				}
			}
		}
		if org == "" {
			for _, key := range []string{"organization", "org", "org_name"} {
				if v := jsonString(m, key); v != "" {
					org = v
					break
				}
			}
		}
	}

	// Build a canonical identity string from whatever we found.
	// At minimum we need email or accountID to have a useful identity.
	if email == "" && accountID == "" {
		return ""
	}

	// Deterministic format: "email|accountID|org"
	return email + "|" + accountID + "|" + org
}

// decodeBase64Segment decodes a base64url-encoded JWT segment, handling
// missing padding.
func decodeBase64Segment(s string) ([]byte, error) {
	// Add padding if needed
	switch len(s) % 4 {
	case 2:
		s += "=="
	case 3:
		s += "="
	}

	// Try URL encoding first (standard for JWTs), then standard encoding
	if decoded, err := base64DecodeURL(s); err == nil {
		return decoded, nil
	}
	return base64DecodeStd(s)
}

// jsonString extracts a string value from a map, returning empty string if
// the key doesn't exist or isn't a string.
func jsonString(m map[string]interface{}, key string) string {
	if m == nil {
		return ""
	}
	v, ok := m[key]
	if !ok {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return s
}

func base64DecodeURL(s string) ([]byte, error) {
	return base64.URLEncoding.DecodeString(s)
}

func base64DecodeStd(s string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(s)
}

func (v *Vault) safeToolDir(tool string) (string, error) {
	if v == nil || strings.TrimSpace(v.basePath) == "" {
		return "", fmt.Errorf("vault base path is empty")
	}
	tool, err := validateVaultSegment("tool", tool)
	if err != nil {
		return "", err
	}

	baseAbs, err := filepath.Abs(v.basePath)
	if err != nil {
		return "", fmt.Errorf("vault base absolute path: %w", err)
	}

	return filepath.Join(baseAbs, tool), nil
}

func (v *Vault) safeProfileDir(tool, profile string) (string, error) {
	if v == nil || strings.TrimSpace(v.basePath) == "" {
		return "", fmt.Errorf("vault base path is empty")
	}
	tool, err := validateVaultSegment("tool", tool)
	if err != nil {
		return "", err
	}
	profile, err = validateVaultSegment("profile", profile)
	if err != nil {
		return "", err
	}

	baseAbs, err := filepath.Abs(v.basePath)
	if err != nil {
		return "", fmt.Errorf("vault base absolute path: %w", err)
	}

	full := filepath.Join(baseAbs, tool, profile)
	fullAbs, err := filepath.Abs(full)
	if err != nil {
		return "", fmt.Errorf("vault profile absolute path: %w", err)
	}

	baseAbs = filepath.Clean(baseAbs)
	if fullAbs != baseAbs && !strings.HasPrefix(fullAbs, baseAbs+string(os.PathSeparator)) {
		return "", fmt.Errorf("vault profile path escapes base directory")
	}

	return fullAbs, nil
}

func validateVaultSegment(kind, val string) (string, error) {
	val = strings.TrimSpace(val)
	if val == "" {
		return "", fmt.Errorf("%s cannot be empty", kind)
	}
	if val == "." || val == ".." {
		return "", fmt.Errorf("invalid %s: %q", kind, val)
	}
	// Only allow safe characters: alphanumeric, underscore, hyphen, period, and @.
	// This prevents shell injection when profile names are used in shell scripts
	// (e.g., claude.go's setupAPIKeyHelper embeds profile name in bash script).
	// The @ and + characters are safe (no special shell meaning) and useful for email-based profile names.
	// Also prevents filesystem issues and unexpected behavior.
	for _, r := range val {
		if !isNameRune(r) {
			return "", fmt.Errorf("invalid %s: %q (only alphanumeric, underscore, hyphen, period, @, and + allowed)", kind, val)
		}
	}
	if filepath.IsAbs(val) || filepath.VolumeName(val) != "" {
		return "", fmt.Errorf("invalid %s: %q", kind, val)
	}

	return val, nil
}

// ProfileIdentity returns a human-readable identity string for a vault profile
// by reading its auth files and extracting identity-bearing claims (e.g., email,
// account ID). Returns empty string if the identity cannot be determined.
//
// This is used by the doctor command to detect when a named profile and a
// system/backup profile share the same underlying account.
func (v *Vault) ProfileIdentity(tool, profile string) string {
	profileDir := v.ProfilePath(tool, profile)

	switch tool {
	case "codex":
		return v.codexProfileIdentity(profileDir)
	case "claude":
		return v.claudeProfileIdentity(profileDir)
	case "gemini":
		return v.geminiProfileIdentity(profileDir)
	case "agy":
		return v.agyProfileIdentity(profileDir)
	case "kimi":
		// The token may be opaque; `caam backup kimi` records what Kimi's
		// /me reported.
		return profileMetaIdentity(profileDir)
	case "zcode":
		return zcodeProfileIdentity(profileDir)
	case "opencode":
		return v.openCodeProfileIdentity(profileDir)
	default:
		return ""
	}
}

// zcodeProfileIdentity extracts the signed-in Z.ai user's email from a
// zcode vault profile's sealed record. It never returns token material.
func zcodeProfileIdentity(profileDir string) string {
	rec, err := zcodecred.ReadRecord(filepath.Join(profileDir, "credentials.json"))
	if err != nil || rec.UserInfo == nil {
		return profileMetaIdentity(profileDir)
	}
	if rec.UserInfo.Email != "" {
		return rec.UserInfo.Email
	}
	return profileMetaIdentity(profileDir)
}

// agyProfileIdentity extracts the human-readable identity (active Google account
// email) from an Antigravity (agy) vault profile by reading google_accounts.json.
// It never reads the antigravity-oauth-token bytes.
func (v *Vault) agyProfileIdentity(profileDir string) string {
	if email := agyActiveAccount(filepath.Join(profileDir, "google_accounts.json")); email != "" {
		return email
	}
	// google_accounts.json is the legacy Gemini CLI's and may name no active
	// account at all; the identity `caam backup agy` resolved from Google is
	// recorded in meta.json instead.
	return profileMetaIdentity(profileDir)
}

// agyActiveProfileByIdentity is the rotation-tolerant fallback used by
// ActiveProfile for Antigravity: the profile whose google_accounts.json
// names the same active account as the live one is the active profile.
// User-named profiles win over system profiles, which can legitimately carry
// the same account.
func (v *Vault) agyActiveProfileByIdentity(fileSet AuthFileSet, profiles []string) string {
	var live string
	for _, spec := range fileSet.Files {
		if filepath.Base(spec.Path) == "google_accounts.json" {
			live = agyActiveAccount(spec.Path)
			break
		}
	}
	if live == "" {
		return ""
	}
	systemMatch := ""
	for _, profile := range profiles {
		if v.agyProfileIdentity(v.ProfilePath(fileSet.Tool, profile)) != live {
			continue
		}
		if !IsSystemProfile(profile) {
			return profile
		}
		if systemMatch == "" {
			systemMatch = profile
		}
	}
	return systemMatch
}

// agyActiveAccount returns the active Google account email recorded in a
// google_accounts.json, or "".
func agyActiveAccount(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var parsed struct {
		Active string `json:"active"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return ""
	}
	return strings.TrimSpace(parsed.Active)
}

// codexProfileIdentity extracts identity from a Codex vault profile by parsing
// JWT tokens in auth.json and extracting email/account claims.
func (v *Vault) codexProfileIdentity(profileDir string) string {
	authPath := filepath.Join(profileDir, "auth.json")
	data, err := os.ReadFile(authPath)
	if err != nil {
		return ""
	}

	var auth map[string]interface{}
	if err := json.Unmarshal(data, &auth); err != nil {
		return ""
	}

	return extractCodexIdentity(auth)
}

// codexFreshness returns a "how recently were these tokens refreshed" timestamp
// for a Codex auth.json blob, and whether a usable timestamp was found.
//
// ChatGPT/Codex OAuth uses refresh-token rotation with reuse detection: each
// refresh mints a new refresh_token and invalidates the previous one. The live
// ~/.codex/auth.json is therefore the authoritative copy of the current token
// family; a vault snapshot taken before a rotation holds an already-consumed
// refresh_token. Presenting that stale token revokes the whole family and
// bricks the account until interactive re-login.
//
// We derive freshness from, in order of preference:
//  1. the top-level "last_refresh" field that the Codex CLI writes (RFC3339), and
//  2. the maximum JWT "iat" (issued-at) claim across the id_token/access_token.
//
// Returns (zeroTime, false) when no timestamp can be parsed, so callers treat
// an unparseable file as "unknown" and fall back to the normal copy.
func codexFreshness(data []byte) (time.Time, bool) {
	var auth map[string]interface{}
	if err := json.Unmarshal(data, &auth); err != nil {
		return time.Time{}, false
	}

	best := time.Time{}
	found := false

	// 1. Top-level last_refresh (the field the Codex CLI updates on rotation).
	if ts := jsonString(auth, "last_refresh"); ts != "" {
		if t, err := time.Parse(time.RFC3339, ts); err == nil {
			best = t
			found = true
		} else if t, err := time.Parse(time.RFC3339Nano, ts); err == nil {
			best = t
			found = true
		}
	}

	// 2. JWT iat claims from whatever tokens are present.
	tokenSources := []map[string]interface{}{auth}
	if tokens, ok := auth["tokens"].(map[string]interface{}); ok {
		tokenSources = append(tokenSources, tokens)
	}
	for _, src := range tokenSources {
		for _, field := range []string{"id_token", "idToken", "access_token", "accessToken"} {
			token := jsonString(src, field)
			if token == "" {
				continue
			}
			if iat, ok := jwtIssuedAt(token); ok {
				if !found || iat.After(best) {
					best = iat
					found = true
				}
			}
		}
	}

	return best, found
}

// jwtIssuedAt decodes a JWT (without signature verification) and returns its
// "iat" (issued-at) claim as a time.Time. Returns (zeroTime, false) if the
// token is malformed or has no numeric iat claim.
func jwtIssuedAt(token string) (time.Time, bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[1] == "" {
		return time.Time{}, false
	}
	payload, err := decodeBase64Segment(parts[1])
	if err != nil {
		return time.Time{}, false
	}
	var claims map[string]interface{}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return time.Time{}, false
	}
	iat, ok := claims["iat"]
	if !ok {
		return time.Time{}, false
	}
	// JSON numbers decode to float64; some encoders may emit a string.
	switch v := iat.(type) {
	case float64:
		return time.Unix(int64(v), 0).UTC(), true
	case json.Number:
		if n, err := v.Int64(); err == nil {
			return time.Unix(n, 0).UTC(), true
		}
	case string:
		if n, err := time.Parse(time.RFC3339, v); err == nil {
			return n, true
		}
	}
	return time.Time{}, false
}

// codexLiveIsNewer reports whether the LIVE codex auth file at livePath holds
// the same OpenAI identity as the incoming vault snapshot AND was refreshed
// strictly more recently. When true, the restore path must NOT clobber the live
// file: doing so would replay an already-rotated (consumed) refresh_token and
// trip the IdP's reuse detection, revoking the whole token family.
//
// Conservative by construction: any uncertainty (missing/unreadable live file,
// different identity, equal-or-older live timestamp, or an unparseable
// timestamp on either side) returns false so the normal verbatim copy proceeds.
// Real cross-account switches (different identity) and first-time restores
// (no live file) are therefore never blocked.
func codexLiveIsNewer(livePath, snapshotPath string) bool {
	liveData, err := os.ReadFile(livePath)
	if err != nil {
		return false // no live file (or unreadable) -> safe to copy
	}
	snapData, err := os.ReadFile(snapshotPath)
	if err != nil {
		return false // no snapshot -> nothing to compare; let copy fail/handle
	}

	// Only guard when it is unambiguously the SAME account. A different identity
	// is a genuine switch and must overwrite.
	var liveAuth, snapAuth map[string]interface{}
	if json.Unmarshal(liveData, &liveAuth) != nil || json.Unmarshal(snapData, &snapAuth) != nil {
		return false
	}
	liveID := extractCodexIdentity(liveAuth)
	snapID := extractCodexIdentity(snapAuth)
	if liveID == "" || snapID == "" || liveID != snapID {
		return false
	}

	liveTS, liveOK := codexFreshness(liveData)
	snapTS, snapOK := codexFreshness(snapData)
	if !liveOK || !snapOK {
		return false // can't compare -> normal copy
	}

	// Preserve the live file only when it is STRICTLY newer.
	return liveTS.After(snapTS)
}

// Claude account identity (issue #73).
//
// Claude Code rotates BOTH tokens in ~/.claude/.credentials.json in place
// every few hours of use, so anything derived from token bytes stops matching
// its own vault snapshot after the first refresh. The rotation-stable
// identity lives in ~/.claude.json: oauthAccount.accountUuid and
// oauthAccount.emailAddress. Access tokens are opaque (sk-ant-oat…), not
// JWTs, so there is nothing to decode.
//
// The top-level userID is deliberately NOT an identity key: it is a
// per-installation identifier that every account logged in on the same
// machine shares (verified across seven distinct accounts in one vault), so
// keying on it would make unrelated accounts match each other.
//
// The helpers below derive identity KEYS — one per available field, each
// namespaced so a uuid can never collide with an email — and two sides match
// when they share any key. Deriving several keys keeps matching robust when
// one side is an older snapshot that lacks a field the other side carries.

const (
	claudeSettingsFile    = ".claude.json"
	claudeCredentialsFile = ".credentials.json"
)

// claudeIdentityKeys derives namespaced identity keys from a parsed
// .claude.json. Returns nil when the file carries no identity.
func claudeIdentityKeys(root map[string]interface{}) []string {
	var keys []string
	switch acct := root["oauthAccount"].(type) {
	case map[string]interface{}:
		if uuid := strings.ToLower(strings.TrimSpace(jsonString(acct, "accountUuid"))); uuid != "" {
			keys = append(keys, "uuid:"+uuid)
		}
		if email := strings.ToLower(strings.TrimSpace(jsonString(acct, "emailAddress"))); email != "" {
			keys = append(keys, "email:"+email)
		}
	case string:
		// Legacy shape: a bare account string.
		if acct = strings.ToLower(strings.TrimSpace(acct)); acct != "" {
			keys = append(keys, "account:"+acct)
		}
	}
	return keys
}

// claudeIdentityKeysFromFile parses a .claude.json at path and derives its
// identity keys. Unreadable or unparseable files yield nil.
func claudeIdentityKeysFromFile(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var root map[string]interface{}
	if err := json.Unmarshal(data, &root); err != nil {
		return nil
	}
	return claudeIdentityKeys(root)
}

// claudeIdentityLabel picks the human-facing identity out of a key set:
// email, else account uuid, else the legacy account string.
func claudeIdentityLabel(keys []string) string {
	for _, prefix := range []string{"email:", "uuid:", "account:"} {
		for _, key := range keys {
			if strings.HasPrefix(key, prefix) {
				return strings.TrimPrefix(key, prefix)
			}
		}
	}
	return ""
}

// identityKeysIntersect reports whether two key sets share any key.
func identityKeysIntersect(a, b []string) bool {
	if len(a) == 0 || len(b) == 0 {
		return false
	}
	seen := make(map[string]struct{}, len(a))
	for _, key := range a {
		seen[key] = struct{}{}
	}
	for _, key := range b {
		if _, ok := seen[key]; ok {
			return true
		}
	}
	return false
}

// mergeIdentityKeys appends the keys of extra that base does not already hold.
func mergeIdentityKeys(base, extra []string) []string {
	seen := make(map[string]struct{}, len(base))
	for _, key := range base {
		seen[key] = struct{}{}
	}
	for _, key := range extra {
		if _, ok := seen[key]; !ok {
			base = append(base, key)
			seen[key] = struct{}{}
		}
	}
	return base
}

// claudeFileSetPath returns the live path registered in fileSet for the named
// Claude file (by base name), or "" when the set does not include it.
func claudeFileSetPath(fileSet AuthFileSet, name string) string {
	for _, spec := range fileSet.Files {
		if filepath.Base(spec.Path) == name {
			return spec.Path
		}
	}
	return ""
}

// claudeLiveIdentityKeys derives the identity of the currently logged-in
// Claude account from the live ~/.claude.json.
func claudeLiveIdentityKeys(fileSet AuthFileSet) []string {
	settingsPath := claudeFileSetPath(fileSet, claudeSettingsFile)
	if settingsPath == "" {
		return nil
	}
	return claudeIdentityKeysFromFile(settingsPath)
}

// claudeProfileIdentityKeys derives a vault profile's identity from its
// .claude.json snapshot, merged with whatever Backup recorded in meta.json so
// matching survives a missing or unparseable snapshot.
func (v *Vault) claudeProfileIdentityKeys(profileDir string) []string {
	keys := claudeIdentityKeysFromFile(filepath.Join(profileDir, claudeSettingsFile))
	if metaKeys := profileMetaIdentityKeys(profileDir); len(metaKeys) > 0 {
		keys = mergeIdentityKeys(keys, metaKeys)
	}
	return keys
}

// profileMetaIdentityKeys reads the identity_keys Backup stored in a
// profile's meta.json (nil when absent).
func profileMetaIdentityKeys(profileDir string) []string {
	data, err := os.ReadFile(filepath.Join(profileDir, "meta.json"))
	if err != nil {
		return nil
	}
	var meta struct {
		IdentityKeys []string `json:"identity_keys"`
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil
	}
	return meta.IdentityKeys
}

// claudeActiveProfileByIdentity is the rotation-tolerant fallback used by
// ActiveProfile when no profile matches the live token bytes: the profile
// whose recorded account identity matches the live ~/.claude.json is the
// active one. User-named profiles win over system profiles (_backup_* etc.),
// which can legitimately carry the same account.
func (v *Vault) claudeActiveProfileByIdentity(fileSet AuthFileSet, profiles []string) string {
	liveKeys := claudeLiveIdentityKeys(fileSet)
	if len(liveKeys) == 0 {
		return ""
	}

	systemMatch := ""
	for _, profile := range profiles {
		profileKeys := v.claudeProfileIdentityKeys(v.ProfilePath(fileSet.Tool, profile))
		if !identityKeysIntersect(liveKeys, profileKeys) {
			continue
		}
		if !IsSystemProfile(profile) {
			return profile
		}
		if systemMatch == "" {
			systemMatch = profile
		}
	}
	return systemMatch
}

// readClaudeOAuth returns the claudeAiOauth object of a Claude credentials
// file, or ok=false when the file is unreadable, unparseable, or has no
// OAuth block (API-key mode).
func readClaudeOAuth(path string) (oauth map[string]interface{}, ok bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	var root map[string]interface{}
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, false
	}
	oauth, ok = root["claudeAiOauth"].(map[string]interface{})
	return oauth, ok
}

// claudeOAuthExpiry extracts claudeAiOauth.expiresAt as an integer epoch
// (Claude Code writes milliseconds). Any unit works as long as the two files
// being compared use the same one.
func claudeOAuthExpiry(oauth map[string]interface{}) (int64, bool) {
	switch n := oauth["expiresAt"].(type) {
	case float64:
		return int64(n), true
	case json.Number:
		i, err := n.Int64()
		return i, err == nil
	case string:
		i, err := json.Number(strings.TrimSpace(n)).Int64()
		return i, err == nil
	}
	return 0, false
}

// claudeLiveCredentialsIncomplete reports whether the live Claude
// credentials carry an OAuth block that is missing its access or refresh
// token — the residue of a failed refresh. A file without any OAuth block
// (API-key mode) is not considered incomplete.
func claudeLiveCredentialsIncomplete(fileSet AuthFileSet) bool {
	credsPath := claudeFileSetPath(fileSet, claudeCredentialsFile)
	if credsPath == "" {
		return false
	}
	oauth, ok := readClaudeOAuth(credsPath)
	if !ok {
		return false
	}
	return jsonString(oauth, "accessToken") == "" || jsonString(oauth, "refreshToken") == ""
}

// claudeLiveIsNewer reports whether the LIVE Claude credentials at livePath
// belong to the SAME account as the vault snapshot at snapshotPath (profile
// profileDir) AND were refreshed strictly more recently. liveKeys is the
// live account identity captured before Restore touched any file. When true,
// Restore must not clobber the live file: doing so would replay an expired
// access token plus an already-consumed refresh token and force an
// interactive /login (issue #73).
//
// Conservative by construction — any uncertainty (missing/unparseable file,
// unknown identity on either side, different identity, a live file missing a
// token, equal-or-older live expiry) returns false so the normal verbatim
// copy proceeds. Cross-account switches and first-time restores are never
// blocked.
func (v *Vault) claudeLiveIsNewer(liveKeys []string, profileDir, livePath, snapshotPath string) bool {
	liveOAuth, ok := readClaudeOAuth(livePath)
	if !ok {
		return false
	}
	snapOAuth, ok := readClaudeOAuth(snapshotPath)
	if !ok {
		return false
	}

	// Only guard when it is unambiguously the SAME account.
	if !identityKeysIntersect(liveKeys, v.claudeProfileIdentityKeys(profileDir)) {
		return false
	}

	// A live file missing either token is failed-refresh residue, never "newer".
	if jsonString(liveOAuth, "accessToken") == "" || jsonString(liveOAuth, "refreshToken") == "" {
		return false
	}

	liveExp, liveOK := claudeOAuthExpiry(liveOAuth)
	snapExp, snapOK := claudeOAuthExpiry(snapOAuth)
	if !liveOK || !snapOK {
		return false
	}
	return liveExp > snapExp
}

// claudeProfileIdentity extracts the human-facing identity of a Claude vault
// profile: the account identity from its .claude.json snapshot / meta.json
// (email when known), falling back to JWT claims in .credentials.json for
// legacy token formats.
func (v *Vault) claudeProfileIdentity(profileDir string) string {
	if label := claudeIdentityLabel(v.claudeProfileIdentityKeys(profileDir)); label != "" {
		return label
	}

	// Try .credentials.json -- parse JWT from claudeAiOauth.accessToken
	credsPath := filepath.Join(profileDir, claudeCredentialsFile)
	if data, err := os.ReadFile(credsPath); err == nil {
		var root map[string]interface{}
		if err := json.Unmarshal(data, &root); err == nil {
			if oauth, ok := root["claudeAiOauth"].(map[string]interface{}); ok {
				// Try to extract identity from the access token JWT
				for _, key := range []string{"accessToken", "idToken"} {
					if token := jsonString(oauth, key); token != "" {
						if id := identityFromJWT(token); id != "" {
							return id
						}
					}
				}
			}
		}
	}

	return ""
}

// geminiProfileIdentity extracts identity from a Gemini vault profile by
// reading settings.json or oauth_creds.json for email/account information.
func (v *Vault) geminiProfileIdentity(profileDir string) string {
	// Try settings.json
	settingsPath := filepath.Join(profileDir, "settings.json")
	if data, err := os.ReadFile(settingsPath); err == nil {
		var root map[string]interface{}
		if err := json.Unmarshal(data, &root); err == nil {
			// Gemini stores identity in various fields depending on version
			for _, key := range []string{"email", "account", "user_email"} {
				if val := jsonString(root, key); val != "" {
					return val
				}
			}
			// Check nested auth object
			if auth, ok := root["auth"].(map[string]interface{}); ok {
				for _, key := range []string{"email", "account"} {
					if val := jsonString(auth, key); val != "" {
						return val
					}
				}
			}
		}
	}

	// Try oauth_creds.json
	credsPath := filepath.Join(profileDir, "oauth_creds.json")
	if data, err := os.ReadFile(credsPath); err == nil {
		var root map[string]interface{}
		if err := json.Unmarshal(data, &root); err == nil {
			for _, key := range []string{"email", "account", "client_email"} {
				if val := jsonString(root, key); val != "" {
					return val
				}
			}
			// Check for JWT id_token
			if token := jsonString(root, "id_token"); token != "" {
				if id := identityFromJWT(token); id != "" {
					return id
				}
			}
		}
	}

	return ""
}

// vaultProfileCarriesAuth reports whether a vault profile holds a credential
// a restore could install: its required credential file, or — for Claude,
// whose optional files can be the credential (API-key mode via settings.json,
// the desktop token cache) — an optional file that carries auth of its own.
// It is Backup's optionalFilesCarryAuth rule applied to the vault copy, so a
// profile Backup would refuse to write is one Restore refuses to install.
func vaultProfileCarriesAuth(fileSet AuthFileSet, profileDir string) bool {
	for _, spec := range fileSet.Files {
		src := filepath.Join(profileDir, vaultFileName(fileSet.Tool, spec.Path))
		if spec.Required {
			if fileExists(src) {
				return true
			}
			continue
		}
		if fileSet.Tool != "claude" {
			// Every other AllowOptionalOnly tool treats any optional file as
			// an auth state (see optionalFilesCarryAuth).
			if fileExists(src) {
				return true
			}
			continue
		}
		switch {
		case isClaudeUserSettings(fileSet.Tool, spec.Path):
			if claudeUserSettingsCarryAuth(src) {
				return true
			}
		case isClaudeDesktopConfig(fileSet.Tool, spec.Path):
			if _, ok, err := claudeDesktopTokenCache(src); err == nil && ok {
				return true
			}
		case filepath.Base(spec.Path) == "auth.json":
			if fileExists(src) {
				return true
			}
		}
	}
	return false
}

// credentialLessProfileError is the loud refusal for a restore that would
// install settings and no credential — a switch that reports success and
// changes nothing.
func credentialLessProfileError(tool, profile string) error {
	login := "log in"
	switch tool {
	case "claude":
		login = "log in with /login in Claude Code"
	case "codex":
		login = "log in with `codex login`"
	case "agy":
		login = "log in with agy"
	}
	return fmt.Errorf("profile %s/%s holds no credential to install (settings only); %s as that account, then re-capture it with `caam backup %s %s`", tool, profile, login, tool, profile)
}

// isNameRune reports whether r may appear in a tool or profile name:
// alphanumeric, underscore, hyphen, period, @ and +.
func isNameRune(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
		(r >= '0' && r <= '9') || r == '_' || r == '-' || r == '.' || r == '@' || r == '+'
}
