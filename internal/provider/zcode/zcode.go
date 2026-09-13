// Package zcode implements the provider adapter for zcode, Z.ai's coding
// harness (binary `zcode`).
//
// Authentication mechanics (verified against a live install):
//   - `zcode login` (or /login in the TUI) signs in with Z.AI OAuth and
//     writes one shared record, $ZCODE_DATA_BASE_DIR-or-~/.zcode/v2/
//     credentials.json (mode 0600), holding the active provider, the Z.ai
//     access token, the zcode session JWT and the user's profile. Every
//     value is sealed with AES-256-GCM under a per-user secret derived from
//     ZCODE_CREDENTIAL_SECRET or "<platform>:<home>:<user>", so the file is
//     readable by this user's processes on this machine and opaque elsewhere.
//   - No refresh token is stored; zcode's own login renews the session.
//
// Auth file swapping (PRIMARY use case):
//   - Back up credentials.json after logging in with each account.
//   - Restore to switch accounts without a new OAuth flow. The file is
//     captured and restored verbatim; caam decrypts only to read identity
//     and to present the session token to zcode's billing API.
package zcode

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/profile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/provider"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/zcodecred"
)

// Provider implements the zcode adapter.
type Provider struct{}

// New creates a new zcode provider.
func New() *Provider {
	return &Provider{}
}

// ID returns the provider identifier.
func (p *Provider) ID() string {
	return "zcode"
}

// DisplayName returns the human-friendly name.
func (p *Provider) DisplayName() string {
	return "zcode (Z.ai)"
}

// DefaultBin returns the default binary name.
func (p *Provider) DefaultBin() string {
	return "zcode"
}

// SupportedAuthModes returns the authentication modes zcode supports.
func (p *Provider) SupportedAuthModes() []provider.AuthMode {
	return []provider.AuthMode{provider.AuthModeOAuth}
}

// AuthFiles returns the auth file specifications for zcode.
func (p *Provider) AuthFiles() []provider.AuthFileSpec {
	return []provider.AuthFileSpec{
		{
			Path:        zcodecred.DefaultPath(),
			Description: "zcode shared Z.AI login (sealed credential record)",
			Required:    true,
		},
	}
}

// profileCredentials is the record inside an isolated profile: zcode reads
// <base>/.zcode/v2/credentials.json, with the base being the profile home.
func profileCredentials(prof *profile.Profile) string {
	return filepath.Join(prof.HomePath(), ".zcode", "v2", "credentials.json")
}

// PrepareProfile sets up the profile directory structure.
func (p *Provider) PrepareProfile(ctx context.Context, prof *profile.Profile) error {
	if err := os.MkdirAll(filepath.Dir(profileCredentials(prof)), 0700); err != nil {
		return fmt.Errorf("create zcode data dir: %w", err)
	}
	return nil
}

// Env returns the environment for running zcode in this profile's context.
// The sealed record is keyed by HOME and user name, so HOME must be the
// profile's own for a record written there to be readable there.
func (p *Provider) Env(ctx context.Context, prof *profile.Profile) (map[string]string, error) {
	return map[string]string{
		"HOME":                   prof.HomePath(),
		zcodecred.DataBaseDirEnv: prof.HomePath(),
	}, nil
}

// Login runs `zcode login`, the native Z.AI OAuth flow.
func (p *Provider) Login(ctx context.Context, prof *profile.Profile) error {
	env, err := p.Env(ctx, prof)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, p.DefaultBin(), "login")
	cmd.Env = os.Environ()
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	fmt.Println("Starting zcode login (Z.AI OAuth)...")
	return cmd.Run()
}

// Logout removes the profile's credential record.
func (p *Provider) Logout(ctx context.Context, prof *profile.Profile) error {
	if err := os.Remove(profileCredentials(prof)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove credentials.json: %w", err)
	}
	return nil
}

// Status checks the current authentication state of the profile. The
// record is sealed under the real user's secret, which is the same secret
// inside a profile home only when HOME matches; a record that cannot be
// unsealed still counts as present but unreadable.
func (p *Provider) Status(ctx context.Context, prof *profile.Profile) (*provider.ProfileStatus, error) {
	status := &provider.ProfileStatus{HasLockFile: prof.IsLocked()}
	rec, err := zcodecred.ReadRecord(profileCredentials(prof))
	if err != nil {
		if err != zcodecred.ErrNoRecord {
			status.Error = err.Error()
		}
		return status, nil
	}
	status.LoggedIn = rec.LoggedIn()
	if rec.UserInfo != nil {
		status.AccountID = rec.UserInfo.Email
	}
	return status, nil
}

// ValidateProfile checks if the profile is correctly configured.
func (p *Provider) ValidateProfile(ctx context.Context, prof *profile.Profile) error {
	if _, err := os.Stat(prof.HomePath()); os.IsNotExist(err) {
		return fmt.Errorf("home directory missing")
	}
	return nil
}

// DetectExistingAuth detects an existing zcode login. Read-only; it
// unseals only to confirm a login is present and never reports a token.
func (p *Provider) DetectExistingAuth() (*provider.AuthDetection, error) {
	detection := &provider.AuthDetection{Provider: p.ID(), Locations: []provider.AuthLocation{}}
	path := zcodecred.DefaultPath()
	loc := provider.AuthLocation{Path: path, Description: "zcode shared Z.AI login"}

	info, err := os.Stat(path)
	if err != nil {
		if !os.IsNotExist(err) {
			loc.ValidationError = fmt.Sprintf("stat error: %v", err)
		}
		detection.Locations = append(detection.Locations, loc)
		return detection, nil
	}
	loc.Exists = true
	loc.LastModified = info.ModTime()
	loc.FileSize = info.Size()
	rec, err := zcodecred.ReadRecord(path)
	switch {
	case err != nil:
		loc.ValidationError = err.Error()
	case !rec.LoggedIn():
		loc.ValidationError = "logged out (no session token)"
	default:
		loc.IsValid = true
	}
	detection.Locations = append(detection.Locations, loc)
	if loc.IsValid {
		detection.Found = true
		locCopy := loc
		detection.Primary = &locCopy
	}
	return detection, nil
}

// ImportAuth copies a detected record into a profile. The copy is sealed
// under the real user's secret; it is readable inside the profile only when
// the profile runs under the same HOME-derived secret, which Env arranges by
// pointing ZCODE_DATA_BASE_DIR (not the secret) at the profile.
func (p *Provider) ImportAuth(ctx context.Context, sourcePath string, prof *profile.Profile) ([]string, error) {
	info, err := os.Stat(sourcePath)
	if err != nil {
		return nil, fmt.Errorf("source auth file not found: %w", err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("source path is a directory, not a file")
	}
	target := profileCredentials(prof)
	if err := copyFile(sourcePath, target); err != nil {
		return nil, fmt.Errorf("copy credentials.json: %w", err)
	}
	return []string{target}, nil
}

// ValidateToken checks that the profile's record unseals and holds a
// session. Active validation falls back to passive.
func (p *Provider) ValidateToken(ctx context.Context, prof *profile.Profile, passive bool) (*provider.ValidationResult, error) {
	result := &provider.ValidationResult{
		Provider:  p.ID(),
		Profile:   prof.Name,
		Method:    "passive",
		CheckedAt: time.Now(),
	}
	rec, err := zcodecred.ReadRecord(profileCredentials(prof))
	if err != nil {
		result.Error = err.Error()
		return result, nil
	}
	if !rec.LoggedIn() {
		result.Error = "zcode is logged out (no session token)"
		return result, nil
	}
	result.Valid = true
	return result, nil
}

// copyFile copies src to dst atomically with 0600 permissions.
func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0700); err != nil {
		return err
	}
	tmp := dst + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

// Ensure Provider implements the interface.
var _ provider.Provider = (*Provider)(nil)
