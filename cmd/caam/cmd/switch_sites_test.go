package cmd

import (
	"bytes"
	"encoding/json"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/testutil"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/config"
	"github.com/spf13/cobra"
)

// rotatedCodex sets up a Codex home and vault where Account a is captured
// (stale) and signed in (rotated), and b is captured and waiting. Every
// switch site must leave a's vault copy equal to the rotated live file.
type rotatedCodex struct {
	authPath                 string
	stale, rotated, incoming []byte
}

func setupRotatedCodex(t *testing.T) *rotatedCodex {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("CODEX_HOME", filepath.Join(tmp, "codex_home"))
	t.Setenv("CAAM_HOME", filepath.Join(tmp, "caam_home"))
	for _, d := range []string{os.Getenv("CODEX_HOME"), os.Getenv("CAAM_HOME")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	oldVault := vault
	vault = authfile.NewVault(filepath.Join(tmp, "vault"))
	t.Cleanup(func() { vault = oldVault })

	base := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC).Unix()
	r := &rotatedCodex{
		stale:    testutil.SyntheticCodexAuth(t, "a@example.com", "a-stale", base),
		rotated:  testutil.SyntheticCodexAuth(t, "a@example.com", "a-rotated", base+3600),
		incoming: testutil.SyntheticCodexAuth(t, "b@example.com", "b", base),
		authPath: filepath.Join(os.Getenv("CODEX_HOME"), "auth.json"),
	}
	write := func(path string, data []byte) {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(vault.ProfilePath("codex", "a"), "auth.json"), r.stale)
	write(filepath.Join(vault.ProfilePath("codex", "b"), "auth.json"), r.incoming)
	write(r.authPath, r.rotated)
	return r
}

func (r *rotatedCodex) assertSwitchedToB(t *testing.T, site string) {
	t.Helper()
	gotA, _ := os.ReadFile(filepath.Join(vault.ProfilePath("codex", "a"), "auth.json"))
	if string(gotA) != string(r.rotated) {
		t.Fatalf("%s: outgoing a was not re-captured before the switch; vault holds %s", site, gotA)
	}
	gotLive, _ := os.ReadFile(r.authPath)
	if string(gotLive) != string(r.incoming) {
		t.Fatalf("%s: live credential after switch = %s, want b", site, gotLive)
	}
}

// caam run --precheck switches to a better account through the core.
func TestRunPrecheck_SwitchesThroughTheCore(t *testing.T) {
	r := setupRotatedCodex(t)
	if !precheckSwitch("codex", "a", "b", true, config.DefaultSPMConfig(), nil) {
		t.Fatal("precheckSwitch() should report the switch")
	}
	r.assertSwitchedToB(t, "run --precheck")
}

// caam run with no active account vaults an unknown live credential
// before installing the one it picked.
func TestRun_FirstSwitchVaultsAnUnknownLiveCredential(t *testing.T) {
	r := setupRotatedCodex(t)
	base := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC).Unix()
	unknown := testutil.SyntheticCodexAuth(t, "stranger@example.com", "x", base)
	if err := os.WriteFile(r.authPath, unknown, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := switchForRun("codex", "b", config.DefaultSPMConfig(), nil); err != nil {
		t.Fatalf("switchForRun() error = %v", err)
	}
	profiles, _ := vault.List("codex")
	saved := false
	for _, p := range profiles {
		data, _ := os.ReadFile(filepath.Join(vault.ProfilePath("codex", p), "auth.json"))
		if string(data) == string(unknown) {
			saved = true
		}
	}
	if !saved {
		t.Fatalf("the stranger's live credential should be in the vault before b replaces it: %v", profiles)
	}
}

// caam workspace <name> switches every tool through the core.
func TestWorkspaceActivate_SwitchesEachToolThroughTheCore(t *testing.T) {
	r := setupRotatedCodex(t)
	cfg := &config.Config{Workspaces: map[string]map[string]string{"work": {"codex": "b"}}}
	if err := switchWorkspace(cfg, "work"); err != nil {
		t.Fatalf("switchWorkspace() error = %v", err)
	}
	r.assertSwitchedToB(t, "workspace")
}

// caam robot act activate switches through the core and reports it.
func TestRobotAct_SwitchesThroughTheCore(t *testing.T) {
	r := setupRotatedCodex(t)
	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := runRobotAct(cmd, []string{"activate", "codex", "b"}); err != nil {
		t.Fatalf("runRobotAct() error = %v (output %s)", err, out.String())
	}
	var env RobotOutput
	if err := json.Unmarshal(out.Bytes(), &env); err != nil || !env.Success {
		t.Fatalf("robot act should succeed: %v %s", err, out.String())
	}
	r.assertSwitchedToB(t, "robot act")
}

// The dashboard's capture files the identity too, for the providers whose
// credential carries none (Antigravity, Kimi): the account name it was
// given is the email the login reported, and status/ls read it from the
// profile's meta.json exactly as after `caam backup`.
func TestCaptureLiveAccount_RecordsTheIdentityForKimi(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("KIMI_CODE_HOME", filepath.Join(tmp, "kimi"))
	t.Setenv("CAAM_KEYCHAIN", "0")
	credPath := filepath.Join(tmp, "kimi", "credentials", "kimi-code.json")
	if err := os.MkdirAll(filepath.Dir(credPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(credPath, []byte(`{"access_token":"SYNTHETIC","refresh_token":"SYNTHETIC","expires_at":4102444800}`), 0o600); err != nil {
		t.Fatal(err)
	}
	oldVault := vault
	vault = authfile.NewVault(filepath.Join(tmp, "vault"))
	t.Cleanup(func() { vault = oldVault })

	if err := captureLiveAccount("kimi", "k@example.com"); err != nil {
		t.Fatalf("captureLiveAccount: %v", err)
	}
	id := getVaultIdentity("kimi", "k@example.com")
	if id == nil || id.Email != "k@example.com" {
		t.Fatalf("identity after capture = %+v, want k@example.com recorded in meta.json", id)
	}
}
