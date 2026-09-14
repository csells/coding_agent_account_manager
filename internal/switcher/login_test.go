package switcher

import (
	"context"
	"errors"
	caamdb "github.com/Dicklesworthstone/coding_agent_account_manager/internal/db"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/testutil"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A Login is a logout first: the signed-in Account goes into the vault
// with its newest tokens, its live credential is cleared so the agent's
// login has nothing to revoke, the login runs, and the new session is
// captured under the account that signed in.
func TestLogin_CapturesThenClearsThenRuns(t *testing.T) {
	w := newCodexWorld(t)
	base := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC).Unix()
	fresh := testutil.SyntheticCodexAuth(t, "new@example.com", "new", base+7200)

	var liveWhenLoginRan []byte
	res, err := Login(context.Background(), w.vault, w.fileSet, LoginOptions{
		Run: func(ctx context.Context) error {
			liveWhenLoginRan, _ = os.ReadFile(w.livePath)
			w.write(t, w.livePath, fresh) // the agent logs a new account in
			return nil
		},
		Identity: func(ctx context.Context) string { return "new@example.com" },
	})
	if err != nil {
		t.Fatalf("Login() error = %v", err)
	}
	if res.Previous != "a" {
		t.Fatalf("result = %+v, want a re-captured", res)
	}
	if got := w.vaultCopy(t, "a"); got != string(w.rotated) {
		t.Fatalf("a's vault copy was not refreshed before the login; holds %s", got)
	}
	if liveWhenLoginRan != nil {
		t.Fatalf("the login found a live credential to revoke: %s", liveWhenLoginRan)
	}
	if res.Account != "new@example.com" || w.vaultCopy(t, "new@example.com") != string(fresh) {
		t.Fatalf("the new session should be filed under the account that signed in: %+v", res)
	}
}

// A live credential caam cannot match to a vault profile is somebody's
// session: it is filed as a backup, then cleared, so it is neither lost
// nor left for the login to revoke.
func TestLogin_FilesAnUnknownLiveCredentialBeforeClearingIt(t *testing.T) {
	w := newCodexWorld(t)
	base := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC).Unix()
	unknown := testutil.SyntheticCodexAuth(t, "stranger@example.com", "x", base)
	w.write(t, w.livePath, unknown)

	var liveWhenLoginRan []byte
	res, err := Login(context.Background(), w.vault, w.fileSet, LoginOptions{
		Run: func(ctx context.Context) error {
			liveWhenLoginRan, _ = os.ReadFile(w.livePath)
			w.write(t, w.livePath, w.incoming)
			return nil
		},
		Identity: func(ctx context.Context) string { return "b@example.com" },
	})
	if err != nil {
		t.Fatalf("Login() error = %v", err)
	}
	if res.Previous == "" {
		t.Fatalf("the unknown credential should be filed; result %+v", res)
	}
	if got := w.vaultCopy(t, res.Previous); got != string(unknown) {
		t.Fatalf("backup %s holds %s, want the stranger's credential", res.Previous, got)
	}
	if liveWhenLoginRan != nil {
		t.Fatalf("the login found a live credential to revoke: %s", liveWhenLoginRan)
	}
}

// A failed capture or clear means the login is not run at all.
func TestLogin_AbortsWhenCaptureOrClearFails(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	w := newCodexWorld(t)
	aDir := w.vault.ProfilePath("codex", "a")
	if err := os.Chmod(aDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(aDir, 0o700) })
	ran := false
	_, err := Login(context.Background(), w.vault, w.fileSet, LoginOptions{
		Run:      func(ctx context.Context) error { ran = true; return nil },
		Identity: func(ctx context.Context) string { return "" },
	})
	if err == nil || ran {
		t.Fatalf("a login must not run when the signed-in account cannot be captured: err=%v ran=%v", err, ran)
	}
	if got := w.read(t, w.livePath); got != string(w.rotated) {
		t.Fatalf("live credential was touched: %s", got)
	}
}

// The login ran but the agent's credential names nobody: the caller has to
// ask for a name, and nothing is filed until it does.
func TestLogin_AsksForANameWhenIdentityIsUnknown(t *testing.T) {
	w := newCodexWorld(t)
	res, err := Login(context.Background(), w.vault, w.fileSet, LoginOptions{
		Run:      func(ctx context.Context) error { w.write(t, w.livePath, w.incoming); return nil },
		Identity: func(ctx context.Context) string { return "" },
	})
	if err != nil {
		t.Fatalf("Login() error = %v", err)
	}
	if !res.NeedsName || res.Account != "" {
		t.Fatalf("result = %+v, want NeedsName with no account filed", res)
	}
	profiles, _ := w.vault.List("codex")
	if len(profiles) != 2 {
		t.Fatalf("nothing should be filed before a name is given: %v", profiles)
	}
	// With a name supplied up front, it is filed under that name.
	res, err = Login(context.Background(), w.vault, w.fileSet, LoginOptions{
		Run:      func(ctx context.Context) error { w.write(t, w.livePath, w.incoming); return nil },
		Identity: func(ctx context.Context) string { return "" },
		Name:     "work",
	})
	if err != nil || res.Account != "work" || res.NeedsName {
		t.Fatalf("a supplied name should be used: %+v %v", res, err)
	}
}

// A login that fails leaves the previous account in the vault and says so.
func TestLogin_ReportsAFailedLogin(t *testing.T) {
	w := newCodexWorld(t)
	_, err := Login(context.Background(), w.vault, w.fileSet, LoginOptions{
		Run:      func(ctx context.Context) error { return errors.New("exit status 1") },
		Identity: func(ctx context.Context) string { return "" },
	})
	var failed *LoginFailedError
	if !errors.As(err, &failed) || failed.Previous != "a" {
		t.Fatalf("err = %v, want LoginFailedError naming the previous account a", err)
	}
	if _, statErr := os.Stat(filepath.Join(w.vault.ProfilePath("codex", "a"), "auth.json")); statErr != nil {
		t.Fatal("a should still be in the vault")
	}
}

// A live credential that matches only a system profile (_original, a
// _backup_) is a session too. Those copies are immutable, so it is filed
// as a fresh backup and then cleared — never refused, never lost.
func TestLogin_FilesASystemProfileMatchAsAFreshBackup(t *testing.T) {
	w := newCodexWorld(t)
	// Only _original knows the live credential.
	w.write(t, filepath.Join(w.vault.ProfilePath("codex", "_original"), "auth.json"), w.rotated)
	if err := os.RemoveAll(w.vault.ProfilePath("codex", "a")); err != nil { // test fixture only
		t.Fatal(err)
	}
	var liveWhenLoginRan []byte
	res, err := Login(context.Background(), w.vault, w.fileSet, LoginOptions{
		Run: func(ctx context.Context) error {
			liveWhenLoginRan, _ = os.ReadFile(w.livePath)
			w.write(t, w.livePath, w.incoming)
			return nil
		},
		Identity: func(ctx context.Context) string { return "b@example.com" },
	})
	if err != nil {
		t.Fatalf("Login() error = %v", err)
	}
	if res.Previous == "" || res.Previous == "_original" || w.vaultCopy(t, res.Previous) != string(w.rotated) {
		t.Fatalf("the session should be filed under a fresh backup, got %+v", res)
	}
	if liveWhenLoginRan != nil {
		t.Fatalf("the login found a live credential to revoke: %s", liveWhenLoginRan)
	}
	if got := w.vaultCopy(t, "_original"); got != string(w.rotated) {
		t.Fatalf("_original must not be rewritten")
	}
}

// The login is logged as the account's first use when a database is given,
// including when the name was supplied rather than read.
func TestLogin_LogsTheLogin(t *testing.T) {
	w := newCodexWorld(t)
	db, err := caamdb.OpenAt(filepath.Join(t.TempDir(), "caam.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := Login(context.Background(), w.vault, w.fileSet, LoginOptions{
		Run:      func(ctx context.Context) error { w.write(t, w.livePath, w.incoming); return nil },
		Identity: func(ctx context.Context) string { return "" },
		Name:     "named",
		DB:       db,
	}); err != nil {
		t.Fatal(err)
	}
	used, _ := db.LastUsed()
	if used["codex"]["named"].IsZero() {
		t.Fatalf("the login should be in the activity log: %v", used)
	}
}
