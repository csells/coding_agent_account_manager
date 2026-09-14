package cmd

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/config"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/identity"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/switcher"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/tui"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/usage"
)

// tuiHooks wires the interactive TUI to the command layer: switching goes
// through switchProfile (outgoing profile re-captured first, failed
// re-capture aborts) and the detail card's Limits section is fetched with
// the same credential resolution as `caam limits` and `caam monitor`.
func tuiHooks() tui.Hooks {
	return tui.Hooks{
		Switch: func(ctx context.Context, provider, profile string) error {
			_, err := switchProfile(ctx, provider, profile, switchOptions{Quiet: true})
			return err
		},
		Limits:       fetchProfileLimits,
		Health:       getProfileHealth,
		Login:        nativeLoginCommand,
		LiveIdentity: liveAccountIdentity,
		Capture:      captureLiveAccount,
	}
}

// nativeLoginCommand is the provider's own login, run in the real
// terminal against the real (live) credential store — the same command a
// user would type. The hint tells them what the command will want.
func nativeLoginCommand(provider string) (*exec.Cmd, string, error) {
	login, err := loginCommandFor(provider, false)
	if err != nil {
		return nil, "", err
	}
	path, err := exec.LookPath(login.Bin)
	if err != nil {
		return nil, "", fmt.Errorf("%s is not installed (not on PATH)", login.Bin)
	}
	return exec.Command(path, login.Args...), login.Hint, nil
}

// liveAccountIdentity names the account the provider's live credential
// belongs to, from the credential itself where it carries one and from
// the provider's own identity endpoint where it does not. "" means the
// dashboard should ask for a name.
func liveAccountIdentity(ctx context.Context, provider string) string {
	get, ok := tools[provider]
	if !ok {
		return ""
	}
	fileSet := get()
	if vault != nil {
		// Refreshes the keychain mirror on macOS, so the file read below
		// is the credential the login just wrote.
		_, _ = vault.ActiveProfile(fileSet)
	}
	var id *identity.Identity
	path := liveCredentialPath(provider)
	switch provider {
	case "codex":
		id, _ = identity.ExtractFromCodexAuth(path)
	case "claude":
		id, _ = identity.ExtractFromClaudeCredentials(path)
	case "kimi":
		id, _ = identity.ExtractFromKimiCredentials(path)
	case "zcode":
		id, _ = identity.ExtractFromZcodeCredentials(path)
	case "agy":
		id, _ = identity.ExtractFromAgyProfile(filepath.Dir(path))
	case "gemini":
		for _, spec := range fileSet.Files {
			if id, _ = identity.ExtractFromGeminiConfig(spec.Path); id != nil && id.Email != "" {
				break
			}
		}
	case "grok":
		for _, spec := range fileSet.Files {
			if filepath.Base(spec.Path) == "auth.json" {
				id, _ = identity.ExtractFromGrokAuth(spec.Path)
			}
		}
	}
	if id != nil && strings.TrimSpace(id.Email) != "" {
		return strings.TrimSpace(id.Email)
	}
	// Antigravity and Kimi name the account only through their services.
	if provider == "agy" || provider == "kimi" {
		if email, err := resolveProfileIdentity(ctx, provider); err == nil {
			return strings.TrimSpace(email)
		}
	}
	return ""
}

// captureLiveAccount vaults the live credential under name: `caam backup`.
func captureLiveAccount(provider, name string) error {
	get, ok := tools[provider]
	if !ok {
		return fmt.Errorf("unknown agent %s", provider)
	}
	if vault == nil {
		vault = authfile.NewVault(authfile.DefaultVaultPath())
	}
	if err := vault.Backup(get(), name); err != nil {
		return err
	}
	// Antigravity's and Kimi's credentials name nobody; the account name
	// the login reported is the identity, recorded in meta.json as `caam
	// backup` does, so status and ls can read it.
	if (provider == "agy" || provider == "kimi") && strings.Contains(name, "@") {
		if err := vault.RecordProfileIdentity(provider, name, name); err != nil {
			return fmt.Errorf("captured %s/%s but could not record its identity: %w", provider, name, err)
		}
	}
	return nil
}

// captureSignedInAccount re-captures the tool's signed-in account into the
// vault, or files an unknown live credential as a backup — what has to
// happen before anything runs the tool's login. Nothing signed in is fine.
func captureSignedInAccount(tool string) error {
	get, ok := tools[tool]
	if !ok {
		return fmt.Errorf("unknown agent %s", tool)
	}
	if vault == nil {
		vault = authfile.NewVault(authfile.DefaultVaultPath())
	}
	_, err := vault.CaptureSignedIn(get())
	return err
}

// resumeCommands are the commands that reopen a tool's most recent session
// in a pane after the account under it was switched.
var resumeCommands = map[string]string{
	"claude": "claude --continue",
	"codex":  "codex resume --last",
	"gemini": "gemini --resume latest",
	"kimi":   "kimi --continue",
}

// switchToNextAccount switches tool to the next vaulted account by the
// configured rotation, through the switch core, and returns the account
// and the command that resumes a session on its history. With no other
// vaulted account it returns ErrNoOtherAccount.
func switchToNextAccount(ctx context.Context, tool string) (account, resume string, err error) {
	resume, ok := resumeCommands[tool]
	if !ok {
		return "", "", fmt.Errorf("%s has no resume command; switch and restart it by hand", tool)
	}
	get, ok := tools[tool]
	if !ok {
		return "", "", fmt.Errorf("unknown provider %s", tool)
	}
	if vault == nil {
		vault = authfile.NewVault(authfile.DefaultVaultPath())
	}
	fileSet := get()
	profiles, err := vault.List(tool)
	if err != nil {
		return "", "", err
	}
	current, _ := vault.ActiveProfile(fileSet)
	var others []string
	for _, p := range profiles {
		if p != current && !authfile.IsSystemProfile(p) {
			others = append(others, p)
		}
	}
	if len(others) == 0 {
		return "", "", ErrNoOtherAccount
	}
	spmCfg, cfgErr := config.LoadSPMConfig()
	if cfgErr != nil {
		spmCfg = config.DefaultSPMConfig()
	}
	db, _ := getDB()
	selection, err := selectProfileWithRotation(tool, others, current, spmCfg, db)
	if err != nil {
		return "", "", err
	}
	if _, err := switcher.Switch(ctx, vault, fileSet, coreOptions(switcher.Options{Profile: selection.Selected, Config: spmCfg, DB: db, Source: "pane-recover"})); err != nil {
		return "", "", err
	}
	return selection.Selected, resume, nil
}

// ErrNoOtherAccount is returned by switchToNextAccount when the tool has
// only the signed-in account vaulted.
var ErrNoOtherAccount = errors.New("no other account to switch to")

// fetchProfileLimits reads one profile's rate-limit windows: from the live
// credential when the profile is the active one (the tool rotates it in
// place, so the vault copy is stale for exactly that account), else from
// its vault copy. It presents the access token and nothing more; it never
// refreshes or rewrites a credential.
func fetchProfileLimits(ctx context.Context, provider, profile string) (*usage.UsageInfo, error) {
	if !isLimitsProvider(provider) {
		return nil, fmt.Errorf("%s has no usage API", provider)
	}
	lookup := buildCredentialLookup(getVaultDir())
	var cred credentialCandidate
	if lookup.ActiveName != nil && lookup.ActiveName(provider) == profile {
		cred = lookup.inspect(credNamespaceLive, provider, profile)
	}
	if !cred.Found() || cred.Token == "" {
		cred = lookup.inspect(credNamespaceVault, provider, profile)
	}
	if !cred.Found() {
		return nil, fmt.Errorf("no credential captured for this profile (re-run caam backup %s %s)", provider, profile)
	}
	if cred.Token == "" {
		if provider == "opencode" {
			return nil, fmt.Errorf("%s", usage.ErrNoOpenCodeLimitsAPI)
		}
		return nil, fmt.Errorf("credential holds no access token")
	}
	results := usage.NewMultiProfileFetcher().FetchAllProfiles(ctx, provider, map[string]string{profile: cred.Token})
	if len(results) == 0 || results[0].Usage == nil {
		return nil, fmt.Errorf("no usage returned for %s/%s", provider, profile)
	}
	return results[0].Usage, nil
}
