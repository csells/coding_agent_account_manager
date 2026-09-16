> Archived handoff, kept for the record. Real account addresses and the machine's
> user name were replaced with placeholders before this file was published.

# Handoff: build the coding-agent account switcher on the caam fork

Read `../CONTEXT.md` (glossary) and `../specs/adr/0001-build-account-switcher-on-caam-fork.md`
first. Their terms are used below without redefinition: Agent, Account, Active Account,
Capture, Switch, Login, Limits, Window. `caam-handoff-v1-superseded.md` is the
withdrawn earlier version; read it only for the verbatim reproductions it preserves.

## Mission

Turn this fork into Chris's account switcher: for every Agent in scope, Login once per
Account, Capture it, see every Account's Limits across all Agents in one command with
each Agent's Active Account marked, and Switch any Agent to any captured Account
without a new Login. Acceptance covers six Agents: **Codex CLI, Claude Code,
Antigravity CLI, Kimi Code, zcode, OpenCode.** Everything else caam already does stays
exactly as it is.

Done means the acceptance block at the bottom passes on this machine against Chris's
real Accounts — all of them, not one per Agent.

## Boundaries

- You own `/Users/<user>/code/forks/Dicklesworthstone/coding_agent_account_manager`
  (`main`, HEAD `ef01b64`; `origin` = `<user>/coding_agent_account_manager`,
  `upstream` = `Dicklesworthstone/coding_agent_account_manager`). Everything in scope is
  caam's own mission — its README already promises "log in once… switch instantly… no
  browser, no OAuth dance," and it already ships a cross-provider `limits` table and a
  cross-tool `status` view. **Stay in caam's idiom and command surface.** Keep every
  commit upstreamable (ADR-0001, revised); whether to open PRs upstream is Chris's call,
  not yours — do not open any.
- **Remove nothing from caam.** No provider, command, flag, file, or test is deleted or
  disabled. Gemini CLI, Grok, Cursor and anything else you don't touch stay as they
  are; they are simply not part of Chris's acceptance. This is bug-fixing and
  hole-filling, not pruning.
- **Do not modify `LICENSE`** (MIT with OpenAI/Anthropic rider, sha256 prefix
  `32a82e0a5754`). No relicensing, no rider removal, no added attribution.
- The switcher **may** initiate Logins through each Agent's native flow. It must never
  store a credential it obtained any other way.
- Live logins on this machine are in daily use. Read them freely; write to
  `~/.codex/auth.json`, the login keychain, `~/.kimi-code/credentials/`,
  `~/.zcode/v2/credentials.json`, or OpenCode's store **only through the switcher's own
  Switch path under test**, and only after the safety rule below is implemented.
- `~/.codex/auth.json` sha256 prefix is `77fa1d78dc92` at handoff time. Report its value
  before and after any run that could touch Codex.
- Reference tools installed here, read-only for you: `codexbar` (`~/.local/bin`),
  `cswap` (`uv`), `codex-multi-auth`, `codex-quota` (`~/.local/bin`). Their source is
  under `~/.local/share/uv/tools/claude-swap/…` and
  `/private/tmp/claude-501/…/scratchpad/cbsrc` (CodexBar Swift clone; may be gone —
  re-clone `steipete/CodexBar` if you need it).

## Standing rules

From the repo's `AGENTS.md` — read it in full:
- Never delete a file or directory without express written permission, including ones
  you created.
- Never run `git reset --hard`, `git clean -fd`, `rm -rf`, or anything that deletes or
  overwrites code/data, unless the user supplies the exact command and accepts the
  consequences in the same message.
- Work on `main`; after pushing `main`, also `git push origin main:master`.
- `make build` (`go build -o caam ./cmd/caam`), `make test` (`go test -v ./...`),
  `make test-race`, `make lint` (`golangci-lint run ./...`). Go `1.26.0`.

Working rules for this task:
- Red-green: a failing test for each behavior before the change. Use
  `internal/testutil.FakeKeychain` (`internal/testutil/keychain.go:101`) for keychain
  paths; never touch the real keychain from tests.
- No secrets in logs, fixtures, commits, or the JSON output. Print identities (email,
  plan, account id), never tokens.
- A `Required` credential that cannot be obtained is a **loud error**, never a silent
  success. The bug that motivated this work was a backup that returned 0 and vaulted
  nothing.
- Small commits, one capability each, so the history reads as a design.

## The safety rule that shapes Switch

caam's `README.md:1091`, verbatim: *"Claude and Codex subscription OAuth uses a
**rotating refresh-token family**: every refresh consumes the current refresh token, and
replaying a stale copy from another machine can trip the provider's reuse detection and
revoke the whole family (this bricked a real account — see issue #19)."* Antigravity
(Google OAuth) rotates as well.

Therefore **Switch must first re-Capture the outgoing Active Account** — overwrite its
vault copy with the live credential — before installing the incoming one, so the vault
never holds a stale chain. For Codex also honor the existing `--reload-daemon`
(`caam activate --help`: SIGTERM a running `codex app-server`/`mcp-server`, which caches
auth in memory). Limits are refreshed **when the user asks** — on invocation, never by a
background daemon. A `--warm` daemon may come later as an opt-in; do not build it now.

Accepted risk (Chris, 2026-09-13): exercising Switch on real Accounts can, on a race,
force one re-Login of that Account. Test Switch on the least important Codex Account
first; Chris has six.

## Files to read first, in order

1. `AGENTS.md`.
2. `cmd/caam/cmd/root.go` — `tools` map :58-64 (`codex`, `claude`, `gemini`, `agy`,
   `grok`, `opencode` → `authfile.*AuthFiles`), provider registry :160-161, `backup`
   command :762, backup RunE ~:900-930, `toolsToCheck` :902.
3. `internal/authfile/authfile.go` — `ClaudeAuthFiles()` :82, `AntigravityAuthFiles()`
   :190-197, `pullClaudeKeychain` call sites :432, :801, :1087, :1188.
4. `internal/authfile/keychain.go` — `pullClaudeKeychain()` :57-68 (**swallows
   `ErrNoKeychain`/`ErrNotFound` as nil at :63-64**), `pushClaudeKeychain()` :71.
5. `internal/keychain/keychain.go` — `Enabled()` :60-68 (on by default on darwin;
   `CAAM_KEYCHAIN=0` disables), `run()` :78-100 (`/usr/bin/security`, 60s timeout),
   `Get()` :140-146, `LoginAccount()` :185 (OS username — `<user>` here).
6. `internal/keychain/claude.go` — `ReadClaude()` :22-34, `EnsureMirror()` :70-80,
   `cachedMirror()` :100 (in-memory TTL), `ensureMirror()` :132-143.
7. `internal/provider/provider.go` — `Provider` interface, `Registry` :156-180;
   `internal/provider/agy/agy.go` — `AuthFiles()` :132, `TokenPath()` :100,
   `DetectExistingAuth()` :308, `ValidateToken()` :468;
   `internal/provider/opencode/opencode.go` — `AuthFiles()` :70 expects
   `~/.local/share/opencode/auth.json`.
8. `cmd/caam/cmd/limits.go:341` (`limitsProviders`), `cmd/caam/cmd/limits_source.go`
   :136/:142/:154/:161/:232/:234 (per-provider `case` blocks).
9. `internal/usage/usage.go` — `UsageInfo` :66, `Fetcher` :136
   (`Fetch(ctx, accessToken) (*UsageInfo, error)`); `internal/usage/codex.go` :30 and
   :106 (template); `internal/usage/claude.go` `applyLimits` :107 (per-model
   allowances); `internal/usage/multi.go` :264 `ReadClaudeCredentials`, :318
   `ReadCodexCredentials`.
10. `internal/discovery/watcher.go:607-614` — the only place `spec.Required` is
    enforced today.

## Verified state of this machine (2026-09-13)

| Agent | Binary | Where the live credential is | caam today |
|---|---|---|---|
| Codex CLI | `/opt/homebrew/bin/codex` | `~/.codex/auth.json` (`cli_auth_credentials_store = "file"` in `~/.codex/config.toml`) | backup ✅ limits ✅ |
| Claude Code | `~/.local/bin/claude` | keychain item service `Claude Code-credentials`, account `<user>`; **no** `~/.claude/.credentials.json` | backup silently vaults no credential; limits empty |
| Antigravity CLI | `~/.local/bin/agy` | keychain item service `gemini`, account `antigravity`; **no** `~/.gemini/antigravity-cli/antigravity-oauth-token` | backup errors (file not found); no limits support |
| Kimi Code | `/opt/homebrew/bin/kimi` | `~/.kimi-code/credentials/kimi-code.json` (mode 0600; keys `access_token`, `refresh_token`, `expires_at`, `scope`, `token_type`, `expires_in`) | no adapter |
| zcode | `/opt/homebrew/bin/zcode` | `~/.zcode/v2/credentials.json` (keys `oauth:active_provider`, `oauth:zai:access_token`, `zcodejwttoken`, `oauth:zai:user_info`) | no adapter |
| OpenCode | `/opt/homebrew/bin/opencode` | `~/.local/share/opencode/opencode.db`, tables `account(id,email,url,access_token,refresh_token,token_expiry,…)` and `credential(id,integration_id,label,value,…)`; **0 rows** — no OpenCode login here yet; `auth.json` absent | adapter expects the absent `auth.json` |

`security find-generic-password -s "Claude Code-credentials" -a <user> -w` exits 0
non-interactively and the value begins with `{` (JSON). Verify the same for the
`gemini`/`antigravity` item before assuming its shape.

Chris's Accounts to cover (all on paid plans): Codex — `alice@example.com`,
`bob.builders@example.com`, `carol@example.com`, `dave@example.com`,
`erin@example.com`, `ops+alice-claude-1@example.com`; Claude — one Account
(`alice@example.com`); Antigravity — one (`alice@example.com`); Kimi, zcode, OpenCode —
whatever he logs into during acceptance.

## Work list

### A. Make the existing cross-Agent views complete

These already exist and are the product's primary surface — do not add a new command:
- `caam limits` with no argument prints one table across providers:
  `PROFILE  SCORE  PRIMARY  SECONDARY  RESETS IN  BURN/HR  DEPLETES  STATUS`
  (verified live today: rows for `claude/…` and `codex/…`; `--format json` exists).
- `caam status` prints the Active Account per tool:
  `TOOL  PROFILE  EMAIL  PLAN  STATUS`.

The job is to make both complete and truthful for all six Agents: every captured
Account appears in `limits` with each Window's used share and reset; `status` shows a
real email and plan for every Agent, not `unknown`. Where an Agent has no usage API
(possibly OpenCode — see G), show that explicitly rather than an empty cell. Design
changes only where needed to answer *which Account should I use next for this Agent?*

### B. Claude Code: Capture vaults no credential on a keychain-only Mac

Reproduction:
```
$ caam backup claude alice@example.com
Backed up claude auth to profile 'alice@example.com'
$ ls ~/.local/share/caam/vault/claude/alice@example.com/
config.json meta.json settings.json          # no .credentials.json
$ caam limits claude
No profiles found.
```
`meta.json` recorded `files: 3` and a resolved identity — success reported, credential
absent. A second observation sharpens it: `caam status` resolves the Claude plan as
`Pro` (so the live keychain read works somewhere in caam), while `caam limits` shows
`claude/alice@example.com … error: unauthorized: tok…` and `status` flags
`🔴 Critical - Token expired`. The bridge reads the live item; the vault/limits path
is working from something stale or absent. Follow that split. `ClaudeAuthFiles()` declares `~/.claude/.credentials.json` `Required: true`; the
keychain bridge is reachable from backup via `pullClaudeKeychain` (`authfile.go:432`)
→ `EnsureMirror` → `ReadClaude` → `Get(ClaudeService, "<user>")` →
`/usr/bin/security find-generic-password -s "Claude Code-credentials" -a <user> -w`,
which succeeds from a shell. Something in caam's invocation returns
`ErrNotFound`/`ErrNoKeychain` and `authfile/keychain.go:63-64` swallows it.

Instrument `run()` and `ReadClaude` (log exit code and stderr, never the value), find
the branch that actually fails, fix it, and make a missing Required credential fail
loudly. Reference implementation that reads this item today: `claude-swap`
(`…/claude_swap/macos_keychain.py:116` — `find-generic-password -a <account> -w -s
<service>`, exit 44 = not found; `credentials.py:117`).

### C. Antigravity: Capture reads a file that does not exist here

Reproduction:
```
$ caam backup agy alice@example.com
Error: no auth files found for agy - login first using the tool's login command
```
`AntigravityAuthFiles()` (`authfile.go:190-197`) and `agy.AuthFiles()` look only for
`~/.gemini/antigravity-cli/antigravity-oauth-token`; the credential is the keychain item
**service `gemini`, account `antigravity`**. Add a keychain bridge modeled on
`pullClaudeKeychain`/`pushClaudeKeychain`, keep the file path working where the file
exists, and leave the `gemini` *CLI* provider (`internal/provider/gemini`, ID `gemini`)
exactly as it is — it is a different Agent from `agy` and must not be conflated with it.

### D. Antigravity Limits

`caam limits agy` → `Error: limits not supported for provider: agy (supported: claude,
codex)`. Implement `internal/usage/agy.go` satisfying `usage.Fetcher`, add the
credential reader beside `ReadCodexCredentials`, register `agy` in `limitsProviders`
and the three `case` sites. The quota call, from a private Gas City repo you cannot
fetch (`gascity/manifold`, `crates/manifold-adapters/src/http/gemini_usage.rs`), verbatim
minus comments:

```rust
const DEFAULT_GEMINI_QUOTA_API_URL: &str =
    "https://cloudcode-pa.googleapis.com/v1internal:retrieveUserQuota";
#[derive(Deserialize)] #[serde(rename_all = "camelCase")]
pub struct QuotaBucket { remaining_fraction: Option<f64>, reset_time: Option<String>,
                         model_id: Option<String>, token_type: Option<String> }
#[derive(Deserialize)] pub struct GeminiQuotaResponse { #[serde(default)] buckets: Vec<QuotaBucket> }
// POST {} with `Authorization: Bearer <access_token>`, Content-Type application/json.
// 401/403 => token expired. remaining_fraction defaults to 1.0 when absent; skip
// buckets without model_id; percent_left = remaining_fraction * 100; rank by model
// tier (pro → primary, flash → secondary, flash-lite after flash).
```
Map buckets onto `UsageInfo` with `UsedPercent = 100 − percent_left` and the reset from
`reset_time` (RFC 3339). Keep per-model detail, as the Claude fetcher does.

### E. Kimi Code adapter (new)

Credential: `~/.kimi-code/credentials/kimi-code.json` — a plain OAuth token file with
`access_token`/`refresh_token`/`expires_at`. Add `internal/provider/kimi`, an
`authfile.KimiAuthFiles`, and the `tools` map entry. Limits: CodexBar's Kimi provider
calls `https://www.kimi.com/apiv2/kimi.gateway.billing.v1.BillingService/GetUsages` and
`…/kimi.gateway.membership.v2.MembershipService/GetSubscriptionStats`; read that
provider's request/response handling in the CodexBar source and port it.

### F. zcode adapter (new)

Credential: `~/.zcode/v2/credentials.json`, keys `oauth:active_provider`,
`oauth:zai:access_token`, `zcodejwttoken`, `oauth:zai:user_info`. zcode is Z.ai's
harness; limits come from Z.ai's coding-plan endpoints — CodexBar's ZAI provider uses
`https://bigmodel.cn/coding-plan/personal/usage` and
`…/coding-plan/team/usage-stats`; port from there. Check whether the refresh token
lives in `credentials.json` or only in `~/.zcode/cli/db/db.sqlite` before deciding what
Capture must copy.

### G. OpenCode adapter: point it at the real store

caam's adapter expects `~/.local/share/opencode/auth.json`; this OpenCode stores auth
in `opencode.db` (`account` and `credential` tables — schema above). Make Capture and
Switch work against the DB (copy it aside before reading; it is WAL-mode). No OpenCode
Account exists here yet — Chris will Login during acceptance. Limits: CodexBar's
OpenCode provider only shows `opencode.ai/_server` and `/workspace/` URLs; the usage
endpoint is **unknown** — find it in CodexBar's source or OpenCode's, and if there is
none, report "no limits API" honestly rather than fabricating a row.

### H. Switch, with the safety rule

Implement Switch per the rule above for all six Agents: re-Capture outgoing → install
incoming → for Codex, `--reload-daemon`. Add a test that proves the outgoing vault
copy was refreshed before the overwrite. Then exercise it for real, least-important
Codex Account first.

## Acceptance (all on this machine, all must hold)

```
$ make build && make test && make test-race && make lint     # green

# Capture every Account Chris has, per Agent, after native Login:
$ caam backup codex <each of the six emails>                 # exit 0 each
$ caam backup claude alice@example.com                       # exit 0; vault has a credential file
$ caam backup agy alice@example.com                          # exit 0; vault has the keychain credential
$ caam backup kimi <email>  /  caam backup zcode <email>  /  caam backup opencode <email>

# The product view:
$ caam limits                                                # every Agent, every Account,
                                                              # each Window's used% + reset
$ caam limits --format json                                   # strict JSON, no secrets
$ caam status                                                # Active Account per Agent, real email + plan

# Per-Agent detail still works:
$ caam limits codex | caam limits claude | caam limits agy | caam limits kimi | caam limits zcode

# Switch, proven:
$ caam activate codex <least-important email> --reload-daemon
$ codex login status                                         # reports the switched Account
$ caam activate codex <original email> --reload-daemon      # and back, no re-login
$ caam activate claude alice@example.com                     # no-op swap succeeds without prompt

# Nothing you didn't mean to touch:
$ git diff --stat LICENSE                                    # empty
$ security find-generic-password -s "Claude Code-credentials" -a <user> -w >/dev/null; echo $?   # 0
```

A Capture that exits 0 without vaulting a credential fails acceptance regardless of the
rest.

## Pending — do not resolve these yourself

- **License rider.** Whether Gas City counts as acting for OpenAI/Anthropic. Business
  question; not yours.
- **`--warm` background refresh.** Explicitly deferred.
- **Claude-Code-style triggers/alerts.** Not a requirement; the word entered the thread
  by accident. Do not build thresholds or notifications.
