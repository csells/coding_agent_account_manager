package monitor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/usage"
)

type fakeFetcher struct {
	usages map[string]*usage.UsageInfo
}

func (f *fakeFetcher) FetchAllProfiles(ctx context.Context, provider string, profiles map[string]string) []usage.ProfileUsage {
	results := make([]usage.ProfileUsage, 0, len(profiles))
	for name := range profiles {
		key := provider + "/" + name
		info := f.usages[key]
		if info == nil {
			info = &usage.UsageInfo{
				Provider:    provider,
				ProfileName: name,
				Error:       "missing usage",
				FetchedAt:   time.Now(),
			}
		}
		results = append(results, usage.ProfileUsage{
			Provider:    provider,
			ProfileName: name,
			Usage:       info,
		})
	}
	return results
}

func TestMonitorRefreshBuildsState(t *testing.T) {
	tmpDir := t.TempDir()
	vault := authfile.NewVault(tmpDir)

	writeProfileFile(t, vault, "claude", "alice", ".credentials.json", `{"claudeAiOauth":{"accessToken":"tok-claude"}}`)
	writeProfileFile(t, vault, "codex", "bob", "auth.json", `{"tokens":{"access_token":"tok-codex"}}`)

	fetcher := &fakeFetcher{
		usages: map[string]*usage.UsageInfo{
			"claude/alice": {
				Provider:    "claude",
				ProfileName: "alice",
				PrimaryWindow: &usage.UsageWindow{
					UsedPercent: 80,
				},
			},
			"codex/bob": {
				Provider:    "codex",
				ProfileName: "bob",
				PrimaryWindow: &usage.UsageWindow{
					UsedPercent: 20,
				},
			},
		},
	}

	mon := NewMonitor(
		WithVault(vault),
		WithFetcher(fetcher),
		WithProviders([]string{"claude", "codex"}),
		WithHealthStore(nil),
	)

	if err := mon.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error: %v", err)
	}

	state := mon.GetState()
	if state == nil {
		t.Fatal("GetState() returned nil")
	}
	if len(state.Profiles) != 2 {
		t.Fatalf("Profiles count = %d, want 2", len(state.Profiles))
	}

	claude := state.Profiles["claude/alice"]
	if claude == nil {
		t.Fatal("missing claude/alice profile")
	}
	if claude.Alert == nil || claude.Alert.Type != AlertWarning {
		t.Fatalf("claude alert = %v, want warning", claude.Alert)
	}

	codex := state.Profiles["codex/bob"]
	if codex == nil {
		t.Fatal("missing codex/bob profile")
	}
	if codex.Alert != nil {
		t.Fatalf("codex alert = %v, want nil", codex.Alert)
	}
}

func TestMonitorRefreshUnsupportedProvider(t *testing.T) {
	tmpDir := t.TempDir()
	vault := authfile.NewVault(tmpDir)
	writeProfileDir(t, vault, "gemini", "carol")

	mon := NewMonitor(
		WithVault(vault),
		WithFetcher(&fakeFetcher{}),
		WithProviders([]string{"gemini"}),
		WithHealthStore(nil),
	)

	if err := mon.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error: %v", err)
	}

	state := mon.GetState()
	profile := state.Profiles["gemini/carol"]
	if profile == nil || profile.Usage == nil || profile.Usage.Error == "" {
		t.Fatalf("expected usage error for gemini profile, got %+v", profile)
	}
}

func TestMonitorRefreshClaudeFallbackAuth(t *testing.T) {
	tmpDir := t.TempDir()
	vault := authfile.NewVault(tmpDir)

	writeProfileFile(t, vault, "claude", "legacy", ".claude.json", `{"oauthToken":"tok-legacy"}`)

	fetcher := &fakeFetcher{
		usages: map[string]*usage.UsageInfo{
			"claude/legacy": {
				Provider:    "claude",
				ProfileName: "legacy",
				PrimaryWindow: &usage.UsageWindow{
					UsedPercent: 10,
				},
			},
		},
	}

	mon := NewMonitor(
		WithVault(vault),
		WithFetcher(fetcher),
		WithProviders([]string{"claude"}),
		WithHealthStore(nil),
	)

	if err := mon.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error: %v", err)
	}

	state := mon.GetState()
	profile := state.Profiles["claude/legacy"]
	if profile == nil || profile.Usage == nil || profile.Usage.Error != "" {
		t.Fatalf("expected legacy claude profile usage, got %+v", profile)
	}
}

func writeProfileFile(t *testing.T, vault *authfile.Vault, provider, profile, name, contents string) {
	t.Helper()
	dir := vault.ProfilePath(provider, profile)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

func writeProfileDir(t *testing.T, vault *authfile.Vault, provider, profile string) {
	t.Helper()
	dir := vault.ProfilePath(provider, profile)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
}

// recordingFetcher notes the token it was handed for each profile so a test
// can prove which credential a row was built from.
type recordingFetcher struct {
	fakeFetcher
	seen map[string]string
}

func (r *recordingFetcher) FetchAllProfiles(ctx context.Context, provider string, profiles map[string]string) []usage.ProfileUsage {
	if r.seen == nil {
		r.seen = make(map[string]string)
	}
	for name, token := range profiles {
		r.seen[provider+"/"+name] = token
	}
	return r.fakeFetcher.FetchAllProfiles(ctx, provider, profiles)
}

// TestMonitorRefreshMarksActiveAndReadsItsLiveCredential: the active
// profile's row is starred and built from the credential the tool has in
// force, not from the vault copy that froze at activate time.
func TestMonitorRefreshMarksActiveAndReadsItsLiveCredential(t *testing.T) {
	tmpDir := t.TempDir()
	vault := authfile.NewVault(tmpDir)
	writeProfileFile(t, vault, "codex", "a", "auth.json", `{"tokens":{"access_token":"vault-a"}}`)
	writeProfileFile(t, vault, "codex", "b", "auth.json", `{"tokens":{"access_token":"vault-b"}}`)

	fetcher := &recordingFetcher{}
	mon := NewMonitor(
		WithVault(vault),
		WithFetcher(fetcher),
		WithProviders([]string{"codex"}),
		WithHealthStore(nil),
		WithActiveProfile(func(provider string) string { return "b" }),
		WithLiveCredential(func(provider, name string) (string, bool) {
			if provider == "codex" && name == "b" {
				return "live-b", true
			}
			return "", false
		}),
	)
	_ = mon.Refresh(context.Background())

	state := mon.GetState()
	if !state.Profiles["codex/b"].Active || state.Profiles["codex/a"].Active {
		t.Fatalf("active flags wrong: a=%v b=%v", state.Profiles["codex/a"].Active, state.Profiles["codex/b"].Active)
	}
	if fetcher.seen["codex/b"] != "live-b" {
		t.Fatalf("active row fetched with %q, want the live credential", fetcher.seen["codex/b"])
	}
	if fetcher.seen["codex/a"] != "vault-a" {
		t.Fatalf("inactive row fetched with %q, want its vault copy", fetcher.seen["codex/a"])
	}
}

// TestMonitorRefreshActiveFallsBackToVaultCopy: when the live credential
// cannot be read, the active profile's row is built from its vault copy,
// and it is still the starred row.
func TestMonitorRefreshActiveFallsBackToVaultCopy(t *testing.T) {
	tmpDir := t.TempDir()
	vault := authfile.NewVault(tmpDir)
	writeProfileFile(t, vault, "codex", "a", "auth.json", `{"tokens":{"access_token":"vault-a"}}`)
	writeProfileFile(t, vault, "codex", "b", "auth.json", `{"tokens":{"access_token":"vault-b"}}`)

	fetcher := &recordingFetcher{}
	asked := 0
	mon := NewMonitor(
		WithVault(vault),
		WithFetcher(fetcher),
		WithProviders([]string{"codex", "claude"}),
		WithHealthStore(nil),
		WithActiveProfile(func(provider string) string { asked++; return "b" }),
		WithLiveCredential(func(provider, name string) (string, bool) { return "", false }),
	)
	_ = mon.Refresh(context.Background())

	state := mon.GetState()
	if !state.Profiles["codex/b"].Active || state.Profiles["codex/a"].Active {
		t.Fatalf("active flags wrong: a=%v b=%v", state.Profiles["codex/a"].Active, state.Profiles["codex/b"].Active)
	}
	if fetcher.seen["codex/b"] != "vault-b" {
		t.Fatalf("active row fetched with %q, want its vault copy when nothing is live", fetcher.seen["codex/b"])
	}
	// The hook is asked once per provider with profiles, not once per
	// loop that needs the answer; claude has no profiles and is not asked.
	if asked != 1 {
		t.Fatalf("active profile asked %d times, want once", asked)
	}
}

// TestMonitorRefreshReadsEveryProviderWithACredentialReader: the monitor
// used to know only claude and codex; the other providers with usage APIs
// are read through the same credential readers `caam limits` uses.
func TestMonitorRefreshReadsEveryProviderWithACredentialReader(t *testing.T) {
	tmpDir := t.TempDir()
	vault := authfile.NewVault(tmpDir)
	writeProfileFile(t, vault, "kimi", "k", "kimi-code.json", `{"access_token":"tok-kimi","refresh_token":"r","expires_at":4102444800}`)
	writeProfileDir(t, vault, "cursor", "c")
	writeProfileFile(t, vault, "claude", "settings-only", "settings.json", `{}`)

	fetcher := &recordingFetcher{}
	mon := NewMonitor(WithVault(vault), WithFetcher(fetcher), WithProviders([]string{"kimi", "cursor", "claude"}), WithHealthStore(nil))
	_ = mon.Refresh(context.Background())

	if fetcher.seen["kimi/k"] != "tok-kimi" {
		t.Fatalf("kimi credential not read: %q", fetcher.seen["kimi/k"])
	}
	state := mon.GetState()
	if p := state.Profiles["cursor/c"]; p == nil || p.Usage == nil || p.Usage.Error == "" {
		t.Fatalf("cursor (no usage API) should be an explicit error row: %+v", p)
	}
	// A profile captured without a credential file says so, not "open ...:
	// no such file or directory".
	p := state.Profiles["claude/settings-only"]
	if p == nil || p.Usage == nil || !strings.Contains(p.Usage.Error, "no credential captured") {
		t.Fatalf("credential-less profile error = %+v", p)
	}
}

// TestMonitorRefreshNeverWritesTheVault: polling presents the access token
// it already holds and nothing else. A refresh must not touch a single
// vault file, because rewriting a credential is how a rotating
// refresh-token family gets replayed.
func TestMonitorRefreshNeverWritesTheVault(t *testing.T) {
	tmpDir := t.TempDir()
	vault := authfile.NewVault(tmpDir)
	writeProfileFile(t, vault, "claude", "alice", ".credentials.json", `{"claudeAiOauth":{"accessToken":"tok-claude","refreshToken":"rt","expiresAt":4102444800000}}`)
	writeProfileFile(t, vault, "codex", "bob", "auth.json", `{"tokens":{"access_token":"tok-codex","refresh_token":"rt"}}`)

	snapshot := func() map[string]string {
		out := make(map[string]string)
		_ = filepath.Walk(tmpDir, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return err
			}
			data, _ := os.ReadFile(path)
			out[path] = info.ModTime().String() + "|" + string(data)
			return nil
		})
		return out
	}
	before := snapshot()

	mon := NewMonitor(WithVault(vault), WithFetcher(&fakeFetcher{}), WithProviders([]string{"claude", "codex"}), WithHealthStore(nil))
	for i := 0; i < 3; i++ {
		_ = mon.Refresh(context.Background())
	}

	after := snapshot()
	if len(after) != len(before) {
		t.Fatalf("refresh changed the vault's file set: %d -> %d files", len(before), len(after))
	}
	for path, want := range before {
		if after[path] != want {
			t.Fatalf("refresh modified %s", path)
		}
	}
}
