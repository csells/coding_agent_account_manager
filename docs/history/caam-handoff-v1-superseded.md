> Archived handoff, kept for the record. Real account addresses and the machine's
> user name were replaced with placeholders before this file was published.

# Handoff: finish caam for Claude Code + Codex CLI + Antigravity CLI on a keychain-only Mac

## Mission

Make `caam backup`, `caam activate`, and `caam limits` work for all three tools —
`claude`, `codex`, `agy` (Antigravity) — on a macOS machine where Claude Code and
Antigravity keep their credentials in the login keychain, not in files. Done means
the acceptance commands at the bottom pass on this machine against real logins.

## Boundaries

- You own `/Users/<user>/code/forks/Dicklesworthstone/coding_agent_account_manager`
  (branch `main`, HEAD `ef01b64`, remotes: `origin` = `<user>/coding_agent_account_manager`,
  `upstream` = `Dicklesworthstone/coding_agent_account_manager`).
- **Do not modify `LICENSE`** (MIT with OpenAI/Anthropic rider, sha256 prefix `32a82e0a5754`).
  Do not relicense, do not remove the rider, do not add attribution.
- Do not touch `~/.codex/auth.json`, `~/.claude/`, `~/.gemini/`, or any keychain item
  except through caam's own code paths under test. The live logins on this machine are
  in use. `~/.codex/auth.json` sha256 prefix `77fa1d78dc92` — verify it is unchanged
  after every run that touches Codex.
- Never run `caam activate` against the live `~/.codex/auth.json` account unless the
  vault profile you activate is that same account (a no-op swap). Activating a
  different Codex account rotates refresh tokens; see "Constraints".
- `codex-quota`, `codexbar`, `cswap`, `codex-multi-auth` are installed in
  `~/.local/bin` / `/opt/homebrew/bin` for reference only. Do not modify them.

## Standing rules you are bound by

From the repo's `AGENTS.md` (read it in full first):
- Never delete any file or directory without express written permission — including
  files you created.
- Never run `git reset --hard`, `git clean -fd`, `rm -rf`, or anything that can delete
  or overwrite code/data, unless the user supplies the exact command and states they
  accept the consequences in the same message.
- All work on `main`; after pushing `main`, also `git push origin main:master`.
- Build: `make build` (`go build -o caam ./cmd/caam`). Test: `make test`
  (`go test -v ./...`), `make test-race`. Lint: `make lint` (`golangci-lint run ./...`).
  Go `1.26.0`.

Repository conventions observed in the code: providers implement
`internal/provider.Provider` and register in `cmd/caam/cmd/root.go:160-161`; the
CLI's tool name → auth-file-set map is `cmd/caam/cmd/root.go:58-64`; keychain access
goes only through `internal/keychain` (`/usr/bin/security`, 60s timeout,
`CAAM_KEYCHAIN=0` disables, on by default on darwin — `internal/keychain/keychain.go:47,52,60-68`).

## Files to read first, in order

1. `AGENTS.md` — the rules above, verbatim.
2. `internal/keychain/keychain.go` — `Enabled()` :60, `Get()` :140-146, `run()` :78-100,
   `LoginAccount()` :185 (returns the OS username, `<user>` here).
3. `internal/keychain/claude.go` — `ReadClaude()` :22-34, `EnsureMirror()` :70-80,
   `cachedMirror()` :100 (in-memory, TTL), `ensureMirror()` :132-143.
4. `internal/authfile/keychain.go` — `pullClaudeKeychain()` :57-68. Note lines 63-64:
   `ErrNoKeychain` and `ErrNotFound` are **swallowed as nil**.
5. `internal/authfile/authfile.go` — `ClaudeAuthFiles()` :82, `AntigravityAuthFiles()`
   :190-197, and the four `pullClaudeKeychain` call sites :432, :801, :1087, :1188.
6. `cmd/caam/cmd/root.go` — `tools` map :58-64, `backup` command :762, backup RunE ~:900-930.
7. `cmd/caam/cmd/limits.go:341` (`limitsProviders`) and
   `cmd/caam/cmd/limits_source.go:136,142,154,161,232,234` (the provider `case` blocks).
8. `internal/usage/usage.go:66` (`UsageInfo`), `:136` (`Fetcher` interface);
   `internal/usage/codex.go:30,106` (`NewCodexFetcher`, `Fetch`) — the template;
   `internal/usage/multi.go:264,318` (`ReadClaudeCredentials`, `ReadCodexCredentials`).
9. `internal/provider/agy/agy.go` — `AuthFiles()` :132, `TokenPath()` :100,
   `DetectExistingAuth()` :308, `ValidateToken()` :468.

## Verified state of this machine (2026-09-13)

- Claude Code credential: keychain item service `Claude Code-credentials`, account
  `<user>`. `security find-generic-password -s "Claude Code-credentials" -a <user> -w`
  exits 0 non-interactively and the value begins with `{` (a JSON object).
  `~/.claude/.credentials.json` does **not** exist.
- Antigravity credential: keychain item service `gemini`, account `antigravity`.
  `~/.gemini/antigravity-cli/antigravity-oauth-token` does **not** exist.
- Codex credential: `~/.codex/auth.json`, `cli_auth_credentials_store = "file"` in
  `~/.codex/config.toml`. Works with caam today.
- `caam 0.1.18 (21ac483)` installed at `~/.local/bin/caam` from the sha256-verified
  release tarball. Vault at `~/.local/share/caam/vault/`.

## The work list — three gaps, with evidence

### 1. `caam backup claude` succeeds but vaults no credential on a keychain-only Mac

Reproduction (run as-is):

```
$ caam backup claude alice@example.com
Backed up claude auth to profile 'alice@example.com'
  Vault: /Users/<user>/.local/share/caam/vault/claude/alice@example.com
$ ls ~/.local/share/caam/vault/claude/alice@example.com/
config.json meta.json settings.json
$ ls ~/.claude/.credentials.json
ls: /Users/<user>/.claude/.credentials.json: No such file or directory
$ caam limits claude
No profiles found.
$ caam ls claude
PROFILE               EMAIL                     PLAN        STATUS
● alice@example.com     unknown                   unknown     🟡 Warning
```

`meta.json` records `files: 3`, `type: user`, and a resolved identity
(`identity_keys: ["uuid:…", "email:alice@example.com"]`) — i.e. the backup believes it
succeeded. `ClaudeAuthFiles()` declares `~/.claude/.credentials.json` as
`Required: true` (`internal/provider/claude/claude.go:150-158` shows the same spec), yet
its absence neither fails the backup nor is bridged from the keychain.

The intended bridge exists: backup → `pullClaudeKeychain` (`authfile.go:432`) →
`keychain.EnsureMirror` → `ReadClaude` → `Get(ClaudeService, "<user>")` →
`/usr/bin/security find-generic-password -s "Claude Code-credentials" -a <user> -w`.
That exact `security` call works from a shell here. Something in caam's invocation
returns `ErrNotFound`/`ErrNoKeychain`, and `authfile/keychain.go:63-64` turns that into
a silent nil.

Do not guess the cause — instrument and observe. Two candidates to rule in or out:
(a) the `security` call succeeds in a shell but fails under the `caam` process
(keychain ACL / TCC denial for a freshly installed unsigned binary, or a 60s prompt
timeout at `keychain.go:78-100`); (b) `cachedMirror` (`claude.go:100`, in-memory TTL)
is memoizing an early failure within one process. Log the actual `security` exit
code and stderr from `run()` (never the secret) and fix the real branch.

What "fixed" means: after `caam backup claude <email>` on this machine, the vault
directory contains a credentials file that `ReadClaudeCredentials`
(`internal/usage/multi.go:264`) can parse (`claudeAiOauth.accessToken` /
`access_token` / `accessToken`), `caam ls claude` shows a real email and plan, and
`caam limits claude` prints windows. A missing `Required: true` file with no keychain
fallback must be a **loud failure**, never a silent success — that silence is what
made this bug invisible.

Reference implementation that reads this item correctly today: `claude-swap`
(`cswap 0.26.0`, installed via `uv tool install claude-swap`), source at
`~/.local/share/uv/tools/claude-swap/lib/python3.13/site-packages/claude_swap/macos_keychain.py`
(`get_password` at :116 uses `find-generic-password -a <account> -w -s <service>`, exit
44 = not found; note its caveat at :26 that `-w` hex-encodes non-printable data) and
`credentials.py:117` (`CLAUDE_CODE_KEYCHAIN_SERVICE = "Claude Code-credentials"`).

### 2. `caam backup agy` fails: Antigravity credential is in the keychain, not a file

Reproduction:

```
$ caam backup agy alice@example.com
Error: no auth files found for agy - login first using the tool's login command
```

`AntigravityAuthFiles()` (`internal/authfile/authfile.go:190-197`) and
`agy.AuthFiles()` (`internal/provider/agy/agy.go:132`, `TokenPath()` :100) only look
for `~/.gemini/antigravity-cli/antigravity-oauth-token` (+ `google_accounts.json`,
`oauth_creds.json`). On this machine that file does not exist; the credential is the
keychain item **service `gemini`, account `antigravity`**.

Add a keychain bridge for Antigravity modeled on `pullClaudeKeychain`
(`internal/authfile/keychain.go:57`) and the `internal/keychain` helpers, so that
backup mirrors the keychain item into the vault and activate writes it back (the
Claude push side is `pushClaudeKeychain`, `authfile/keychain.go:71`). Keep the
existing file-based path working for machines where the file exists. Same rule as
gap 1: a `Required: true` credential that cannot be obtained from file or keychain is
a loud error.

Note `caam status` currently reports `gemini (logged in, no matching profile…)` — the
`gemini` tool is the separate Gemini CLI provider (`internal/provider/gemini`, ID
`gemini`). Antigravity is `agy`. Do not conflate them.

### 3. `caam limits` does not support Antigravity

Reproduction:

```
$ caam limits agy
Error: limits not supported for provider: agy (supported: claude, codex)
```

`cmd/caam/cmd/limits.go:341`: `var limitsProviders = []string{"claude", "codex"}`.
Dispatch is by string `case` in `cmd/caam/cmd/limits_source.go` at :136/:142 (candidate
credential paths), :154/:161 (credential readers), :232/:234 (fetchers).

Implement `internal/usage/agy.go` satisfying `usage.Fetcher`
(`usage.go:136`: `Fetch(ctx, accessToken string) (*UsageInfo, error)`), using
`internal/usage/codex.go` (`NewCodexFetcher` :30, `Fetch` :106) as the template, and
add an `agy` credential reader beside `ReadCodexCredentials` (`multi.go:318`). Then
register `agy` in `limitsProviders` and the three `case` sites.

The quota endpoint and response shape are already worked out in a private Gas City
repo (`gascity/manifold`, `crates/manifold-adapters/src/http/gemini_usage.rs`). You
cannot fetch that repo; here is the relevant excerpt, verbatim, minus comments:

```rust
const DEFAULT_GEMINI_QUOTA_API_URL: &str =
    "https://cloudcode-pa.googleapis.com/v1internal:retrieveUserQuota";

#[derive(Debug, Clone, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct QuotaBucket {
    #[serde(default)] pub remaining_fraction: Option<f64>,
    #[serde(default)] pub reset_time: Option<String>,
    #[serde(default)] pub model_id: Option<String>,
    #[serde(default)] pub token_type: Option<String>,
}
#[derive(Debug, Clone, Deserialize)]
pub struct GeminiQuotaResponse { #[serde(default)] pub buckets: Vec<QuotaBucket> }
pub struct GeminiModelQuota { pub model_id: String, pub percent_left: f64, pub reset_time: Option<String> }
pub struct GeminiUsageResult { pub primary: Option<GeminiModelQuota>, pub secondary: Option<GeminiModelQuota> }

pub fn fetch_gemini_usage(access_token: &str) -> anyhow::Result<GeminiUsageResult> {
    let response = reqwest::blocking::Client::new()
        .post(&gemini_quota_api_url())
        .header("Authorization", format!("Bearer {}", access_token))
        .header("Content-Type", "application/json")
        .body("{}")
        .send()?;
    if response.status().as_u16() == 401 || response.status().as_u16() == 403 {
        anyhow::bail!("Gemini quota API returned status {}: token may be expired", response.status());
    }
    if !response.status().is_success() { anyhow::bail!("…{}: {}", response.status(), response.text().unwrap_or_default()); }
    let quota: GeminiQuotaResponse = response.json()?;
    Ok(collapse_buckets(&quota.buckets))
}
// collapse_buckets: skips buckets without model_id; remaining_fraction defaults to
// 1.0 when absent; ranks by model tier (pro → primary, flash → secondary, flash-lite
// after flash); percent_left = remaining_fraction * 100.
```

Map `model_id` buckets onto `UsageInfo.PrimaryWindow` / `SecondaryWindow` with
`UsedPercent = 100 - percent_left` and the window's reset from `reset_time`
(RFC 3339). Preserve per-model detail if `UsageInfo` can carry it; the Claude fetcher
already tracks per-model allowances (`internal/usage/claude.go`, `applyLimits` :107),
so follow that pattern rather than flattening.

## Constraints specific to this work

- **Refresh-token rotation is real and will brick accounts.** Codex, Gemini/Antigravity
  and Grok rotate refresh tokens on use; replaying a stale copy trips reuse detection
  and revokes the whole family (upstream README cites issue #19). Anything that
  activates a Codex profile must honor `--reload-daemon` (SIGTERM a running
  `codex app-server`) and must not leave two live copies of one account's refresh
  chain. Never test activate against a Codex account other than the one already live
  in `~/.codex/auth.json`.
- Claude Code's keychain blob is JSON; do not add hex/base64 decoding on assumption —
  observe the real bytes first (`security … -w | head -c 1` is `{` here).
- No secrets in logs, test fixtures, commit messages, or this handoff's successors.
  Use `internal/testutil.FakeKeychain` for tests (`internal/testutil/keychain.go:101`).
- Tests: red-green. Add a failing test for each gap before the fix
  (`internal/authfile`, `internal/keychain`, `internal/usage`, `cmd/caam/cmd`).
  `make test` and `make lint` must pass. Run `make test-race` before pushing.
- Keep upstream mergeability: small commits, one gap each, no unrelated refactors.
  The user has not decided fork-vs-upstream; write the fixes so either is possible.

## Acceptance criteria (all on this machine, all must hold)

```
$ make build && make test && make lint            # all green

$ caam backup claude alice@example.com            # exits 0
$ ls ~/.local/share/caam/vault/claude/alice@example.com/
                                                   # contains a credentials file
$ caam limits claude                              # prints ≥1 window with a reset time
$ caam ls claude                                  # EMAIL and PLAN are not "unknown"

$ caam backup agy alice@example.com               # exits 0, vaults the keychain credential
$ caam limits agy                                 # prints Antigravity quota per model with reset
$ caam ls agy                                     # profile listed with identity

$ caam backup codex ops+alice-claude-1@example.com  # still works
$ caam limits codex                               # still prints PRIMARY/SECONDARY/RESETS
$ shasum -a 256 ~/.codex/auth.json | cut -c1-12   # 77fa1d78dc92 (unchanged)

$ security find-generic-password -s "Claude Code-credentials" -a <user> -w >/dev/null; echo $?   # 0, still readable
$ git -C ~/code/forks/Dicklesworthstone/coding_agent_account_manager diff --stat LICENSE          # empty
```

A silent success — a backup that returns 0 with no credential in the vault — is a
failure of gap 1 even if every other line passes.

## Pending decisions (do not resolve these yourself)

- Fork under `gascity/` vs. upstream PRs to Dicklesworthstone. Unresolved. Keep
  commits upstreamable either way.
- Whether Gas City is a "Restricted Party" under the license rider (it restricts
  OpenAI/Anthropic and anyone acting on their behalf). Business question, not yours.
- Whether to adopt manifold's window model (`vendor + account + window_kind` with
  rollover detection) in place of caam's Primary/Secondary pair. Out of scope unless
  the user asks.
