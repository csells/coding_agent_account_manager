package authfile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/keychain"
)

// This file bridges the macOS login keychain into the file-shaped auth-file
// model. On macOS, Claude Code keeps its OAuth blob as a generic password and
// only falls back to ~/.claude/.credentials.json when the keychain is
// unreachable, so without the bridge `Backup` snapshots a profile with no
// token and `Restore` swaps files the CLI ignores (issue #98). The
// Antigravity CLI does the same with its Google OAuth token (service
// "gemini", account "antigravity") and the antigravity-oauth-token file.
//
// The keychain is authoritative and the credentials file is its mirror: every
// existing path that hashes, dedupes, or expiry-checks the file keeps working
// untouched. On a host with no login keychain (non-darwin, an isolated HOME,
// CAAM_KEYCHAIN=0) each helper is inert.
//
// pullKeychain, pushKeychain and clearKeychain dispatch on the file set's tool
// and are the only entry points the vault calls; the per-tool helpers below
// them decide whether a given file set is bridged at all.

// claudeKeychainPath returns the credentials file the login keychain should be
// bridged to, or "" when the bridge does not apply to this file set.
//
// The item `security` reaches lives in $HOME/Library/Keychains, so it belongs
// to the credentials file under the *current* HOME and to no other. A file set
// pointing somewhere else — a profile-scoped HOME, a fixture — is left to the
// files it names, which is also what makes shallow profiles keep working: they
// run under their own HOME, which has no login keychain.
func claudeKeychainPath(fileSet AuthFileSet) string {
	if fileSet.Tool != "claude" || !keychain.Enabled() {
		return ""
	}
	credPath := claudeFileSetPath(fileSet, claudeCredentialsFile)
	if credPath == "" {
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	if filepath.Clean(credPath) != filepath.Join(home, ".claude", claudeCredentialsFile) {
		return ""
	}
	return credPath
}

// pullClaudeKeychain refreshes the live ~/.claude/.credentials.json mirror
// from the login keychain before a caller reads it.
//
// It returns nil when there is nothing to bridge — no keychain, no item, or a
// file set that does not register the credentials file — and a descriptive
// error only when the keychain refused access or the mirror could not be
// written. Callers that merely inspect state ignore the error; the ones that
// capture or hand over credentials surface it.
func pullClaudeKeychain(fileSet AuthFileSet) error {
	credPath := claudeKeychainPath(fileSet)
	if credPath == "" {
		return nil
	}
	if _, err := keychain.EnsureMirror(credPath); err != nil {
		if errors.Is(err, keychain.ErrNoKeychain) || errors.Is(err, keychain.ErrNotFound) {
			return nil
		}
		return fmt.Errorf("read Claude credentials from the macOS login keychain: %w", err)
	}
	return nil
}

// pushClaudeKeychain writes the just-restored credentials file into the login
// keychain, which is what actually changes the account Claude Code uses.
//
// A failure here is fatal to the switch: reporting success while the keychain
// still holds the previous account is exactly the silent no-op of issue #98.
func pushClaudeKeychain(fileSet AuthFileSet) error {
	credPath := claudeKeychainPath(fileSet)
	if credPath == "" {
		return nil
	}
	if !fileExists(credPath) {
		// An API-key or helper-based profile carries no OAuth blob, and the
		// restore left the live files alone; leave the keychain alone too, so
		// the bridge stays exactly as (in)active as the file path it mirrors.
		return nil
	}
	if err := keychain.PushMirror(credPath); err != nil {
		if errors.Is(err, keychain.ErrNoKeychain) {
			return nil
		}
		return fmt.Errorf("write Claude credentials to the macOS login keychain: %w", err)
	}
	return nil
}

// clearClaudeKeychain removes the Claude item as part of a logout.
func clearClaudeKeychain(fileSet AuthFileSet) error {
	if claudeKeychainPath(fileSet) == "" {
		return nil
	}
	if err := keychain.DeleteClaude(); err != nil {
		return fmt.Errorf("remove Claude credentials from the macOS login keychain: %w", err)
	}
	return nil
}

// agyKeychainPath returns the Antigravity token file the keychain item should
// be bridged to, or "" when the bridge does not apply to this file set. As
// for Claude, only the token under the current HOME's default ~/.gemini
// belongs to the login keychain; a GEMINI_HOME pointed elsewhere (a shallow
// lane, a fixture) is left to the file it names.
func agyKeychainPath(fileSet AuthFileSet) string {
	if fileSet.Tool != "agy" || !keychain.Enabled() {
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	want := filepath.Join(home, ".gemini", "antigravity-cli", agyTokenFile)
	for _, spec := range fileSet.Files {
		if filepath.Base(spec.Path) == agyTokenFile && filepath.Clean(spec.Path) == want {
			return spec.Path
		}
	}
	return ""
}

// agyTokenFile is the basename of the Antigravity CLI's authoritative token
// file, the one its keychain item is mirrored onto.
const agyTokenFile = "antigravity-oauth-token"

// pullAgyKeychain refreshes the antigravity-oauth-token mirror from the
// login keychain; same contract as pullClaudeKeychain.
func pullAgyKeychain(fileSet AuthFileSet) error {
	tokenPath := agyKeychainPath(fileSet)
	if tokenPath == "" {
		return nil
	}
	if _, err := keychain.EnsureAgyMirror(tokenPath); err != nil {
		if errors.Is(err, keychain.ErrNoKeychain) || errors.Is(err, keychain.ErrNotFound) {
			return nil
		}
		return fmt.Errorf("read Antigravity credentials from the macOS login keychain: %w", err)
	}
	return nil
}

// pushAgyKeychain writes the just-restored token file into the login
// keychain, which is what changes the account agy uses.
func pushAgyKeychain(fileSet AuthFileSet) error {
	tokenPath := agyKeychainPath(fileSet)
	if tokenPath == "" || !fileExists(tokenPath) {
		return nil
	}
	if err := keychain.PushAgyMirror(tokenPath); err != nil {
		if errors.Is(err, keychain.ErrNoKeychain) {
			return nil
		}
		return fmt.Errorf("write Antigravity credentials to the macOS login keychain: %w", err)
	}
	return nil
}

// clearAgyKeychain removes the Antigravity item as part of a logout.
func clearAgyKeychain(fileSet AuthFileSet) error {
	if agyKeychainPath(fileSet) == "" {
		return nil
	}
	if err := keychain.DeleteAgy(); err != nil {
		return fmt.Errorf("remove Antigravity credentials from the macOS login keychain: %w", err)
	}
	return nil
}

// pullKeychain refreshes the live credential file of a bridged tool from the
// login keychain before a caller reads it. Tools without a keychain item are
// a no-op.
func pullKeychain(fileSet AuthFileSet) error {
	switch fileSet.Tool {
	case "claude":
		return pullClaudeKeychain(fileSet)
	case "agy":
		return pullAgyKeychain(fileSet)
	}
	return nil
}

// pushKeychain writes a bridged tool's just-restored credential file back
// into the login keychain.
func pushKeychain(fileSet AuthFileSet) error {
	switch fileSet.Tool {
	case "claude":
		return pushClaudeKeychain(fileSet)
	case "agy":
		return pushAgyKeychain(fileSet)
	}
	return nil
}

// clearKeychain removes a bridged tool's keychain item as part of a logout.
func clearKeychain(fileSet AuthFileSet) error {
	switch fileSet.Tool {
	case "claude":
		return clearClaudeKeychain(fileSet)
	case "agy":
		return clearAgyKeychain(fileSet)
	}
	return nil
}

func fileExists(path string) bool {
	if path == "" {
		return false
	}
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}
