package switcher

import (
	"context"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/testutil"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/config"
	caamdb "github.com/Dicklesworthstone/coding_agent_account_manager/internal/db"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/health"
)

// codexWorld is a Codex home and a vault in temp dirs: Account a is
// captured (stale copy) and signed in (rotated live file); Account b is
// captured and waiting.
type codexWorld struct {
	vault                    *authfile.Vault
	fileSet                  authfile.AuthFileSet
	livePath                 string
	stale, rotated, incoming []byte
}

func newCodexWorld(t *testing.T) *codexWorld {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("CODEX_HOME", filepath.Join(tmp, "codex_home"))
	if err := os.MkdirAll(os.Getenv("CODEX_HOME"), 0o700); err != nil {
		t.Fatal(err)
	}
	w := &codexWorld{vault: authfile.NewVault(filepath.Join(tmp, "vault")), fileSet: authfile.CodexAuthFiles()}
	base := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC).Unix()
	w.stale = testutil.SyntheticCodexAuth(t, "a@example.com", "a-stale", base)
	w.rotated = testutil.SyntheticCodexAuth(t, "a@example.com", "a-rotated", base+3600)
	w.incoming = testutil.SyntheticCodexAuth(t, "b@example.com", "b", base)
	w.write(t, filepath.Join(w.vault.ProfilePath("codex", "a"), "auth.json"), w.stale)
	w.write(t, filepath.Join(w.vault.ProfilePath("codex", "b"), "auth.json"), w.incoming)
	w.livePath = filepath.Join(os.Getenv("CODEX_HOME"), "auth.json")
	w.write(t, w.livePath, w.rotated)
	return w
}

func (w *codexWorld) write(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func (w *codexWorld) read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func (w *codexWorld) vaultCopy(t *testing.T, profile string) string {
	return w.read(t, filepath.Join(w.vault.ProfilePath("codex", profile), "auth.json"))
}

// A Switch re-captures the outgoing Active Account before the incoming one
// is installed: Codex rotates the refresh-token family in place, so the
// vault must end up holding the rotated tokens, never the consumed ones.
func TestSwitch_RecapturesTheOutgoingAccountFirst(t *testing.T) {
	w := newCodexWorld(t)

	res, err := Switch(context.Background(), w.vault, w.fileSet, Options{Profile: "b", Config: config.DefaultSPMConfig()})
	if err != nil {
		t.Fatalf("Switch() error = %v", err)
	}
	if !res.Recaptured || res.PreviousProfile != "a" {
		t.Fatalf("result = %+v, want a re-captured and named as previous", res)
	}
	if got := w.vaultCopy(t, "a"); got != string(w.rotated) {
		t.Fatalf("outgoing a was not re-captured before the switch; vault holds %s", got)
	}
	if got := w.read(t, w.livePath); got != string(w.incoming) {
		t.Fatalf("live credential after switch = %s, want b", got)
	}
}

// A switch that cannot refresh the outgoing vault copy must not overwrite
// the live credential: that is exactly how the vault ends up holding a
// stale chain. Force says "I know", and proceeds with a warning.
func TestSwitch_AbortsWhenTheOutgoingAccountCannotBeRecaptured(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	w := newCodexWorld(t)
	aDir := w.vault.ProfilePath("codex", "a")
	if err := os.Chmod(aDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(aDir, 0o700) })

	_, err := Switch(context.Background(), w.vault, w.fileSet, Options{Profile: "b"})
	if err == nil {
		t.Fatal("Switch() should refuse when the outgoing account cannot be re-captured")
	}
	if got := w.read(t, w.livePath); got != string(w.rotated) {
		t.Fatalf("live credential was replaced despite the refusal: %s", got)
	}

	res, err := Switch(context.Background(), w.vault, w.fileSet, Options{Profile: "b", Force: true})
	if err != nil {
		t.Fatalf("forced Switch() error = %v", err)
	}
	if res.RecaptureWarning == "" || res.Recaptured {
		t.Fatalf("a forced switch past a failed re-capture must say so: %+v", res)
	}
	if got := w.read(t, w.livePath); got != string(w.incoming) {
		t.Fatalf("forced switch did not install b: %s", got)
	}
}

// A live credential caam cannot match to any vault profile is somebody's
// session: it is filed as an auto-backup before b is installed, never
// destroyed.
func TestSwitch_VaultsAnUnknownLiveCredentialBeforeReplacingIt(t *testing.T) {
	w := newCodexWorld(t)
	base := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC).Unix()
	unknown := testutil.SyntheticCodexAuth(t, "stranger@example.com", "x", base)
	w.write(t, w.livePath, unknown)

	res, err := Switch(context.Background(), w.vault, w.fileSet, Options{Profile: "b"})
	if err != nil {
		t.Fatalf("Switch() error = %v", err)
	}
	if res.AutoBackup == "" {
		t.Fatalf("an unvaulted live credential should be auto-backed up, result %+v", res)
	}
	if got := w.vaultCopy(t, res.AutoBackup); got != string(unknown) {
		t.Fatalf("auto-backup %s holds %s, want the stranger's credential", res.AutoBackup, got)
	}
	if got := w.read(t, w.livePath); got != string(w.incoming) {
		t.Fatalf("live credential after switch = %s, want b", got)
	}
}

// A profile that holds settings and no credential is refused before
// anything moves: installing it would change nothing while reporting a
// switch.
func TestSwitch_RefusesAnAccountWithoutACredential(t *testing.T) {
	w := newCodexWorld(t)
	w.write(t, filepath.Join(w.vault.ProfilePath("codex", "empty"), "config.toml"), []byte("model = \"x\"\n"))

	_, err := Switch(context.Background(), w.vault, w.fileSet, Options{Profile: "empty"})
	if err == nil {
		t.Fatal("Switch() should refuse a credential-less profile")
	}
	if got := w.read(t, w.livePath); got != string(w.rotated) {
		t.Fatalf("live credential was touched by a refused switch: %s", got)
	}
}

// With an activity log, a switch records the incoming account as activated
// and, when the outgoing one is known, as deactivated — what LAST USED and
// `caam history` read.
func TestSwitch_LogsTheSwitch(t *testing.T) {
	w := newCodexWorld(t)
	db, err := caamdb.OpenAt(filepath.Join(t.TempDir(), "caam.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// a was activated earlier, so its deactivation has a duration.
	if err := db.LogEvent(caamdb.Event{Timestamp: time.Now().Add(-time.Hour), Type: caamdb.EventActivate, Provider: "codex", ProfileName: "a"}); err != nil {
		t.Fatal(err)
	}

	if _, err := Switch(context.Background(), w.vault, w.fileSet, Options{Profile: "b", DB: db, Source: "test"}); err != nil {
		t.Fatalf("Switch() error = %v", err)
	}
	used, err := db.LastUsed()
	if err != nil {
		t.Fatal(err)
	}
	if used["codex"]["b"].IsZero() {
		t.Fatalf("b should be logged as activated: %v", used)
	}
	if used["codex"]["a"].Before(time.Now().Add(-time.Minute)) {
		t.Fatalf("a should be logged as deactivated just now: %v", used["codex"]["a"])
	}
}

// fakeRefresher records refresh calls.
type fakeRefresher struct{ calls []string }

func (f *fakeRefresher) Refresh(ctx context.Context, tool, profile string) error {
	f.calls = append(f.calls, tool+"/"+profile)
	return nil
}

// The core refreshes the incoming token only when the one gate says so:
// an expired token is refreshed before it is installed; a valid one is
// not spent.
func TestSwitch_RefreshesOnlyAnExpiredIncomingToken(t *testing.T) {
	w := newCodexWorld(t)
	fr := &fakeRefresher{}
	expiry := map[string]time.Time{"b": time.Now().Add(time.Hour)}
	healthOf := func(tool, profile string) *health.ProfileHealth {
		return &health.ProfileHealth{TokenExpiresAt: expiry[profile]}
	}
	res, err := Switch(context.Background(), w.vault, w.fileSet, Options{Profile: "b", Refresher: fr, HealthOf: healthOf})
	if err != nil || res.Refreshed || len(fr.calls) != 0 {
		t.Fatalf("a valid incoming token must not be refreshed: res=%+v calls=%v err=%v", res, fr.calls, err)
	}

	expiry["a"] = time.Now().Add(-time.Minute)
	res, err = Switch(context.Background(), w.vault, w.fileSet, Options{Profile: "a", Refresher: fr, HealthOf: healthOf})
	if err != nil || !res.Refreshed || len(fr.calls) != 1 || fr.calls[0] != "codex/a" {
		t.Fatalf("an expired incoming token should be refreshed first: res=%+v calls=%v err=%v", res, fr.calls, err)
	}
}

// Analytics off means the switch is not logged, whatever DB is passed, so
// callers can pass the database unconditionally.
func TestSwitch_LogsNothingWhenAnalyticsIsOff(t *testing.T) {
	w := newCodexWorld(t)
	db, err := caamdb.OpenAt(filepath.Join(t.TempDir(), "caam.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cfg := config.DefaultSPMConfig()
	cfg.Analytics.Enabled = false
	if _, err := Switch(context.Background(), w.vault, w.fileSet, Options{Profile: "b", Config: cfg, DB: db}); err != nil {
		t.Fatal(err)
	}
	used, _ := db.LastUsed()
	if len(used) != 0 {
		t.Fatalf("nothing should be logged with analytics off: %v", used)
	}
}
