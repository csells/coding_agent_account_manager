// Package switcher is the one way an Account becomes the Active Account:
// re-capture the outgoing one, abort if that fails, then install the
// incoming credential. Every path in caam that switches — the CLI, the
// dashboard, the monitor, the HTTP API, the wrappers — calls Switch, so
// the rule that keeps rotating refresh-token families intact cannot be
// skipped by one of them. See docs/ACCOUNT_SWITCHER.md §1.
package switcher

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/config"
	caamdb "github.com/Dicklesworthstone/coding_agent_account_manager/internal/db"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/health"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/refresh"
)

// Options describes one switch.
type Options struct {
	// Profile is the Account to make Active.
	Profile string
	// Force proceeds when the outgoing Account cannot be re-captured. The
	// vault is then left with a stale copy of it; the caller has said so.
	Force bool
	// BackupCurrent files the live credential as an auto-backup even when
	// caam knows whose it is (`--backup-current`).
	BackupCurrent bool
	// Config carries the safety settings (auto-backup mode and limit).
	Config *config.SPMConfig
	// DB, when set and Config.Analytics is on, receives the
	// activate/deactivate events the switch makes; Source says what chose
	// the profile (rotation, project, user).
	DB     *caamdb.DB
	Source string
	// Refresher, with HealthOf, refreshes the incoming Account's token before
	// it is installed — only when refresh.NeedsRefresh says so (expired), and
	// only for providers the refresher supports. Both optional.
	Refresher Refresher
	HealthOf  func(tool, profile string) *health.ProfileHealth
}

// Refresher refreshes one Account's token in the vault.
type Refresher interface {
	Refresh(ctx context.Context, tool, profile string) error
}

// RefresherFunc adapts a function to Refresher.
type RefresherFunc func(ctx context.Context, tool, profile string) error

// Refresh calls f.
func (f RefresherFunc) Refresh(ctx context.Context, tool, profile string) error {
	return f(ctx, tool, profile)
}

// Result reports what a switch did.
type Result struct {
	// PreviousProfile is the Account that was Active before, "" if none
	// caam knew.
	PreviousProfile string
	// Recaptured is true when the previous Account's vault copy was
	// refreshed from the live credential.
	Recaptured bool
	// RecaptureWarning is set when re-capture failed and Force proceeded.
	RecaptureWarning string
	// AutoBackup names the auto-backup profile the live credential was
	// filed under before the switch, "" when none was made.
	AutoBackup string
	// AutoBackupWarning is set when an auto-backup was wanted and failed.
	AutoBackupWarning string
	// Refreshed is true when the incoming Account's token was refreshed
	// before it was installed; RefreshWarning says why a wanted refresh did
	// not happen.
	Refreshed      bool
	RefreshWarning string
}

// Switch makes opts.Profile the Active Account for fileSet's tool.
func Switch(ctx context.Context, vault *authfile.Vault, fileSet authfile.AuthFileSet, opts Options) (*Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if opts.Config == nil {
		opts.Config = config.DefaultSPMConfig()
	}
	res := &Result{}
	res.PreviousProfile, _ = vault.ActiveProfile(fileSet)

	// Refresh the incoming token first, when the one gate says it has
	// expired; a refresh spends the refresh token, so never otherwise.
	if opts.Refresher != nil && opts.HealthOf != nil {
		if refresh.NeedsRefresh(opts.HealthOf(fileSet.Tool, opts.Profile), nil) {
			switch err := opts.Refresher.Refresh(ctx, fileSet.Tool, opts.Profile); {
			case err == nil:
				res.Refreshed = true
			case errors.Is(err, refresh.ErrUnsupported):
				res.RefreshWarning = fmt.Sprintf("token not refreshed: %v", err)
			default:
				res.RefreshWarning = fmt.Sprintf("token refresh failed: %v", err)
			}
		}
	}

	// A live credential caam cannot match to a vault profile is somebody's
	// session; installing over it would lose it. File it first ("smart"
	// mode), or always when asked.
	mode := strings.TrimSpace(opts.Config.Safety.AutoBackupBeforeSwitch)
	if mode == "" {
		mode = "smart"
	}
	if opts.BackupCurrent {
		mode = "always"
	}
	wantBackup := false
	switch mode {
	case "always":
		wantBackup = res.PreviousProfile != opts.Profile && authfile.HasAuthFiles(fileSet)
	case "smart":
		wantBackup = res.PreviousProfile == "" && authfile.HasAuthFiles(fileSet)
	}
	if wantBackup {
		name, err := vault.BackupCurrent(fileSet)
		switch {
		case err != nil:
			res.AutoBackupWarning = fmt.Sprintf("could not auto-backup the live credential: %v", err)
		case name != "":
			res.AutoBackup = name
			if max := opts.Config.Safety.MaxAutoBackups; max > 0 {
				_ = vault.RotateAutoBackups(fileSet.Tool, max)
			}
		}
	}

	// Re-capture the outgoing Account's (possibly rotated) tokens into its
	// own vault copy BEFORE the live credential is overwritten. Without
	// this the vault keeps a consumed refresh token, and a later switch
	// back replays it, trips the provider's reuse detection and revokes
	// the whole family (issue #19). A failed re-capture aborts with the
	// live credential untouched; Force is the only override.
	if outgoing := res.PreviousProfile; outgoing != "" && outgoing != opts.Profile {
		if err := vault.ResnapshotOutgoing(fileSet, outgoing, opts.Profile); err != nil {
			if !opts.Force {
				return nil, fmt.Errorf("could not re-capture the outgoing profile %s before switching: %w (the vault would be left with a stale copy of its credential; fix the cause, or force the switch)", outgoing, err)
			}
			res.RecaptureWarning = fmt.Sprintf("could not re-capture outgoing profile %s: %v (proceeding: forced)", outgoing, err)
		} else {
			res.Recaptured = true
		}
	}

	if err := vault.Restore(fileSet, opts.Profile); err != nil {
		return nil, fmt.Errorf("activate failed: %w", err)
	}

	if opts.Config.Analytics.Enabled {
		LogSwitch(opts.DB, fileSet.Tool, res.PreviousProfile, opts.Profile, map[string]any{"previous_profile": res.PreviousProfile, "selection_source": opts.Source})
	}
	return res, nil
}

// LogSwitch records a switch in the activity log: a deactivation of the
// outgoing Account carrying how long it was in use (so `caam usage`
// accrues active time, issue #31), then an activation of the incoming one
// with details. System profiles are never deactivated; they are not
// sessions.
func LogSwitch(db *caamdb.DB, tool, outgoing, incoming string, details map[string]any) {
	if db == nil {
		return
	}
	now := time.Now()
	if outgoing != "" && outgoing != incoming && !authfile.IsSystemProfile(outgoing) {
		if last, err := db.LastActivation(tool, outgoing); err == nil && !last.IsZero() {
			if d := now.Sub(last); d > 0 {
				_ = db.LogEvent(caamdb.Event{
					Type:        caamdb.EventDeactivate,
					Provider:    tool,
					ProfileName: outgoing,
					Timestamp:   now,
					Duration:    d,
					Details:     map[string]any{"switched_to": incoming},
				})
			}
		}
	}
	_ = db.LogEvent(caamdb.Event{
		Type:        caamdb.EventActivate,
		Provider:    tool,
		ProfileName: incoming,
		Timestamp:   now,
		Details:     details,
	})
}
