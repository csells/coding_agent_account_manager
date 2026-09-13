// Package kimi implements the provider adapter for Kimi Code, Moonshot AI's
// coding CLI (binary `kimi`).
//
// Authentication mechanics (verified against a live install):
//   - Kimi Code signs in through an OAuth device flow started with /login
//     inside the CLI and stores the result as a plain OAuth token file at
//     $KIMI_CODE_HOME/credentials/kimi-code.json (default
//     ~/.kimi-code/credentials/kimi-code.json, mode 0600):
//     {"access_token","refresh_token","expires_at","scope","token_type",
//     "expires_in"}. A logged-out install keeps the file with empty tokens.
//   - The CLI renews the access token from the refresh token itself; caam
//     never refreshes it.
//   - ~/.kimi-code/device_id is a stable per-install id the CLI sends with
//     every request; it is not a credential and is not captured.
//
// Auth file swapping (PRIMARY use case):
//   - Back up kimi-code.json after logging in with each account.
//   - Restore to switch accounts without a new device flow.
package kimi

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/profile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/provider"
)

// HomeEnv relocates the Kimi Code home directory.
const HomeEnv = "KIMI_CODE_HOME"

// Provider implements the Kimi Code adapter.
type Provider struct{}

// New creates a new Kimi Code provider.
func New() *Provider {
	return &Provider{}
}

// ID returns the provider identifier.
func (p *Provider) ID() string {
	return "kimi"
}

// DisplayName returns the human-friendly name.
func (p *Provider) DisplayName() string {
	return "Kimi Code (Moonshot AI)"
}

// DefaultBin returns the default binary name.
func (p *Provider) DefaultBin() string {
	return "kimi"
}

// SupportedAuthModes returns the authentication modes Kimi Code supports.
func (p *Provider) SupportedAuthModes() []provider.AuthMode {
	return []provider.AuthMode{
		provider.AuthModeOAuth,
		provider.AuthModeDeviceCode,
	}
}

// Home returns the Kimi Code home directory: KIMI_CODE_HOME, or
// ~/.kimi-code.
func Home() string {
	if override := strings.TrimSpace(os.Getenv(HomeEnv)); override != "" {
		return override
	}
	homeDir, _ := os.UserHomeDir()
	return filepath.Join(homeDir, ".kimi-code")
}

// CredentialsPath returns the token file for a Kimi Code home.
func CredentialsPath(home string) string {
	return filepath.Join(home, "credentials", "kimi-code.json")
}

// AuthFiles returns the auth file specifications for Kimi Code.
func (p *Provider) AuthFiles() []provider.AuthFileSpec {
	return []provider.AuthFileSpec{
		{
			Path:        CredentialsPath(Home()),
			Description: "Kimi Code OAuth token (Kimi For Coding subscription)",
			Required:    true,
		},
	}
}

// profileHome is the Kimi Code home inside an isolated profile.
func profileHome(prof *profile.Profile) string {
	return filepath.Join(prof.HomePath(), ".kimi-code")
}

// PrepareProfile sets up the profile directory structure.
func (p *Provider) PrepareProfile(ctx context.Context, prof *profile.Profile) error {
	if err := os.MkdirAll(filepath.Join(profileHome(prof), "credentials"), 0700); err != nil {
		return fmt.Errorf("create kimi home: %w", err)
	}
	return nil
}

// Env returns the environment for running Kimi Code in this profile's
// context: HOME and KIMI_CODE_HOME both point into the profile, so a stray
// inherited KIMI_CODE_HOME cannot pull the real login back in.
func (p *Provider) Env(ctx context.Context, prof *profile.Profile) (map[string]string, error) {
	return map[string]string{
		"HOME":  prof.HomePath(),
		HomeEnv: profileHome(prof),
	}, nil
}

// Login starts Kimi Code interactively; the user signs in with /login.
func (p *Provider) Login(ctx context.Context, prof *profile.Profile) error {
	env, err := p.Env(ctx, prof)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, p.DefaultBin())
	cmd.Env = os.Environ()
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	fmt.Println("Starting Kimi Code...")
	fmt.Println("Type /login inside Kimi Code to sign in, then exit.")
	return cmd.Run()
}

// Logout clears the profile's token file.
func (p *Provider) Logout(ctx context.Context, prof *profile.Profile) error {
	if err := os.Remove(CredentialsPath(profileHome(prof))); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove kimi-code.json: %w", err)
	}
	return nil
}

// credential is the token file's shape.
type credential struct {
	AccessToken  string      `json:"access_token"`
	RefreshToken string      `json:"refresh_token"`
	ExpiresAt    json.Number `json:"expires_at"`
}

// readCredential parses a token file. A file with empty tokens — what the
// CLI leaves behind after /logout — is not a login.
func readCredential(path string) (*credential, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c credential
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

func (c *credential) loggedIn() bool {
	return c != nil && (strings.TrimSpace(c.AccessToken) != "" || strings.TrimSpace(c.RefreshToken) != "")
}

func (c *credential) expiry() time.Time {
	if c == nil {
		return time.Time{}
	}
	if secs, err := c.ExpiresAt.Float64(); err == nil && secs > 0 {
		return time.Unix(int64(secs), 0)
	}
	return time.Time{}
}

// Status checks the current authentication state of the profile.
func (p *Provider) Status(ctx context.Context, prof *profile.Profile) (*provider.ProfileStatus, error) {
	status := &provider.ProfileStatus{HasLockFile: prof.IsLocked()}
	c, err := readCredential(CredentialsPath(profileHome(prof)))
	if err != nil {
		if !os.IsNotExist(err) {
			status.Error = err.Error()
		}
		return status, nil
	}
	status.LoggedIn = c.loggedIn()
	if exp := c.expiry(); !exp.IsZero() {
		status.ExpiresAt = exp.Format(time.RFC3339)
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

// DetectExistingAuth detects an existing Kimi Code login. Read-only; it
// reports whether the file holds tokens, never the tokens.
func (p *Provider) DetectExistingAuth() (*provider.AuthDetection, error) {
	detection := &provider.AuthDetection{Provider: p.ID(), Locations: []provider.AuthLocation{}}
	path := CredentialsPath(Home())
	loc := provider.AuthLocation{Path: path, Description: "Kimi Code OAuth token"}

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
	c, err := readCredential(path)
	switch {
	case err != nil:
		loc.ValidationError = fmt.Sprintf("invalid JSON: %v", err)
	case !c.loggedIn():
		loc.ValidationError = "logged out (empty tokens)"
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

// ImportAuth copies a detected token file into a profile's Kimi Code home.
func (p *Provider) ImportAuth(ctx context.Context, sourcePath string, prof *profile.Profile) ([]string, error) {
	info, err := os.Stat(sourcePath)
	if err != nil {
		return nil, fmt.Errorf("source auth file not found: %w", err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("source path is a directory, not a file")
	}
	target := CredentialsPath(profileHome(prof))
	if err := copyFile(sourcePath, target); err != nil {
		return nil, fmt.Errorf("copy kimi-code.json: %w", err)
	}
	return []string{target}, nil
}

// ValidateToken checks the token file: present, non-empty, and not past its
// expiry unless a refresh token can renew it. Active validation falls back
// to passive; the CLI owns the refresh flow.
func (p *Provider) ValidateToken(ctx context.Context, prof *profile.Profile, passive bool) (*provider.ValidationResult, error) {
	result := &provider.ValidationResult{
		Provider:  p.ID(),
		Profile:   prof.Name,
		Method:    "passive",
		CheckedAt: time.Now(),
	}
	c, err := readCredential(CredentialsPath(profileHome(prof)))
	if err != nil {
		result.Error = "no Kimi Code token found"
		return result, nil
	}
	if !c.loggedIn() {
		result.Error = "Kimi Code is logged out (empty tokens)"
		return result, nil
	}
	result.ExpiresAt = c.expiry()
	if !result.ExpiresAt.IsZero() && result.ExpiresAt.Before(time.Now()) && strings.TrimSpace(c.RefreshToken) == "" {
		result.Error = "Kimi Code access token expired and no refresh token is stored"
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
