package switcher

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	caamdb "github.com/Dicklesworthstone/coding_agent_account_manager/internal/db"
)

// LoginOptions describes one Login: how to run the agent's native login,
// how to learn who signed in, and where to log it.
type LoginOptions struct {
	// Run runs the agent's native login (the terminal is the agent's until
	// it returns). Required.
	Run func(ctx context.Context) error
	// Identity names the account the live credential belongs to after the
	// login, "" when it cannot tell. Optional.
	Identity func(ctx context.Context) string
	// Name files the new session under this name when Identity gives none.
	Name string
	// Capture files the live credential under a name; defaults to the
	// vault's Backup. The dashboard routes it through its command layer.
	Capture func(name string) error
	// DB, when set, receives a login event for the new account.
	DB *caamdb.DB
}

// LoginResult reports what a Login did.
type LoginResult struct {
	// Previous is the Account that was signed in and re-captured, "" if none
	// caam knew.
	Previous string
	// Cleared is true when Previous's live credential was removed before
	// the login ran.
	Cleared bool
	// Account is the name the new session was filed under; "" with
	// NeedsName when the caller has to ask for one (then Capture it).
	Account   string
	NeedsName bool
}

// LoginFailedError is returned when the agent's login did not complete.
// Previous names the Account that is in the vault and can be restored.
type LoginFailedError struct {
	Previous string
	Err      error
}

func (e *LoginFailedError) Error() string {
	if e.Previous != "" {
		return fmt.Sprintf("login did not complete: %v (the previous account %s is in the vault; switch to it to restore it)", e.Err, e.Previous)
	}
	return fmt.Sprintf("login did not complete: %v", e.Err)
}

func (e *LoginFailedError) Unwrap() error { return e.Err }

// Prepared is the state between PrepareLogin and FinishLogin.
type Prepared struct {
	Previous string
	Cleared  bool
}

// PrepareLogin is the first half of a Login: the signed-in Account goes
// into the vault with its newest tokens, and its live credential is
// cleared, so the agent's login has nothing to revoke. An agent's login is
// a logout first — Codex revokes the session it finds, refresh-token
// family and vault copy included — and a login that finds nothing has
// nothing to revoke. Only a credential whose account is in the vault is
// cleared. A live credential caam cannot match to a vault profile is
// somebody's session too: it is filed as a backup first, then cleared —
// never destroyed, never left for the login to revoke. A failed capture or
// clear is an error and the login must not run.
func PrepareLogin(vault *authfile.Vault, fileSet authfile.AuthFileSet) (*Prepared, error) {
	p := &Prepared{}
	active, _ := vault.ActiveProfile(fileSet)
	switch {
	case active != "":
		p.Previous = active
		if err := vault.Backup(fileSet, active); err != nil {
			return nil, fmt.Errorf("not starting a login: the signed-in account %s could not be re-captured first (%w); its newest tokens would be lost", active, err)
		}
	case authfile.HasAuthFiles(fileSet):
		name, err := vault.BackupCurrent(fileSet)
		if err != nil {
			return nil, fmt.Errorf("not starting a login: the live credential matches no vault profile and could not be filed first (%w); the login would lose it", err)
		}
		if name == "" {
			return p, nil
		}
		p.Previous = name
	default:
		return p, nil
	}
	active = p.Previous
	if err := authfile.ClearAuthFiles(fileSet); err != nil {
		return nil, fmt.Errorf("not starting a login: %s is in the vault but its live credential could not be cleared (%w); the login would revoke it", active, err)
	}
	p.Cleared = true
	return p, nil
}

// FinishLogin is the second half: after the agent's login returned, learn
// who signed in and file the live credential under that account (or the
// given name), logging the login. With neither, NeedsName is set and
// nothing is filed.
func FinishLogin(ctx context.Context, vault *authfile.Vault, fileSet authfile.AuthFileSet, prep *Prepared, opts LoginOptions) (*LoginResult, error) {
	res := &LoginResult{}
	if prep != nil {
		res.Previous, res.Cleared = prep.Previous, prep.Cleared
	}
	name := ""
	if opts.Identity != nil {
		name = strings.TrimSpace(opts.Identity(ctx))
	}
	if name == "" {
		name = strings.TrimSpace(opts.Name)
	}
	if name == "" {
		res.NeedsName = true
		return res, nil
	}
	capture := opts.Capture
	if capture == nil {
		capture = func(name string) error { return vault.Backup(fileSet, name) }
	}
	if err := capture(name); err != nil {
		return res, fmt.Errorf("logged in as %s, but capturing it failed: %w", name, err)
	}
	res.Account = name
	LogLogin(opts.DB, fileSet.Tool, name)
	return res, nil
}

// Login runs the whole sequence: PrepareLogin, opts.Run, FinishLogin.
func Login(ctx context.Context, vault *authfile.Vault, fileSet authfile.AuthFileSet, opts LoginOptions) (*LoginResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if opts.Run == nil {
		return nil, fmt.Errorf("no login to run")
	}
	prep, err := PrepareLogin(vault, fileSet)
	if err != nil {
		return nil, err
	}
	if err := opts.Run(ctx); err != nil {
		return nil, &LoginFailedError{Previous: prep.Previous, Err: err}
	}
	return FinishLogin(ctx, vault, fileSet, prep, opts)
}

// LogLogin records a login in the activity log: the account's first use.
func LogLogin(db *caamdb.DB, tool, name string) {
	if db == nil {
		return
	}
	_ = db.LogEvent(caamdb.Event{
		Timestamp:   time.Now(),
		Type:        caamdb.EventLogin,
		Provider:    tool,
		ProfileName: name,
		Details:     map[string]any{"source": "login"},
	})
}
