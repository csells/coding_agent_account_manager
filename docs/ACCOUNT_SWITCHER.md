# The account switcher: how it works and what it learned

caam's `account-switcher` branch turns caam into the tool the handoff asked
for: log in once with every account on every coding agent, see each
account's rate-limit state, and switch which account an agent uses without
logging in again. This document is the architecture and the hard-won facts
behind it, written so the next person (or agent) does not rediscover them
against a real account. Decisions with alternatives are in the ADRs under
`specs/adr/` next to the repository; the user-facing description is in
`README.md` ("Supported Tools" and the dashboard sections).

Vocabulary follows `CONTEXT.md`: an **Agent** is a CLI coding tool (caam
calls it a provider), an **Account** is one login identity at that Agent's
service (caam calls it a profile), **Capture** brings an Account's credential
into the vault (caam's `backup`), **Switch** makes a captured Account the
Active one (caam's `activate`), **Login** obtains a credential by
authenticating, **Limits** are an Account's Windows and how much of each is
left.

## 1. The one rule everything else follows from

**A credential in the vault is a copy of a session, not a password.** For
Claude Code, Codex and Antigravity the session is an OAuth refresh-token
family that rotates: every refresh consumes the current refresh token and
issues a new one, and presenting a consumed token trips the provider's
reuse detection and revokes the whole family (caam issue #19 bricked a real
account this way). Two consequences drive the design:

- **The vault copy of the Active Account goes stale while the agent runs.**
  The agent refreshes in place; the vault still holds the token it started
  with. So before anything replaces the live credential — a Switch, a Login —
  the outgoing Active Account is **re-captured** into the vault first, and a
  failed re-capture aborts the operation (`authfile.Vault.ResnapshotOutgoing`,
  `cmd/caam/cmd/activate.go: performSwitch`, `--force` to override). Every
  path that switches — `caam activate`, the dashboard's Enter, `caam monitor`'s
  Enter — goes through the same `switchProfile` core so this cannot be
  forgotten in one of them.
- **A vault copy that has been rotated past cannot be revived.** Neither caam
  nor the agent can refresh it; only a new Login mints a new family. So the
  switcher never "refreshes" a credential casually (see §6), and never lets
  an operation invalidate a family it has not just captured (see §2).

The handoff's standing constraints follow from the same rule: caam writes to
`~/.codex/auth.json`, the login keychain, `~/.kimi-code/credentials/`,
`~/.zcode/v2/credentials.json` and OpenCode's store only through its own
Capture/Switch/Clear paths, never ad hoc; tests never touch the real
keychain (`testutil.FakeKeychain`) and every test package runs under
`testutil.IsolatedMain`, which redirects `HOME`.

## 2. A login is a logout first: capture, clear, then log in

Discovered 2026-09-13, at the cost of five Codex Accounts. The Codex CLI's
`codex login` (0.154, `codex-rs/cli/src/login.rs`) calls
`clear_existing_auth_before_login`, which runs `logout_with_revoke` on
whatever session it finds in `$CODEX_HOME/auth.json`. That revokes the
refresh token, which revokes the family, which kills the vault copy taken
seconds earlier. The symptom is `401 token_revoked` from
`chatgpt.com/backend-api/wham/usage` and `refresh_token_invalidated` from the
token endpoint; nothing brings the Account back but a new login.

The rule, implemented in the dashboard's `n` flow
(`internal/tui/newaccount.go: startNewAccountLogin`); upstream's `caam add`
has the same outline but not the same care — it vaults the outgoing account
under an `_auto_backup_` name instead of its own, clears with a raw file
delete that skips the keychain item, and asks for a name instead of reading
identity (audit finding 9):

1. Re-capture the Active Account into the vault (newest tokens).
2. **Clear the live credential** (`authfile.ClearAuthFiles`), so the agent's
   login finds no session and has nothing to revoke.
3. Run the agent's native login (`codex login`, `kimi login`, `zcode login`,
   `opencode auth login`, `grok login`; Claude Code, Gemini CLI and
   Antigravity are started and log in interactively).
4. Read who signed in (`liveAccountIdentity`, per-agent identity extraction,
   falling back to the Antigravity/Kimi identity endpoints) and capture the
   live credential under that name; a credential with no readable identity
   asks for a name.

Step 2 runs only when step 1 succeeded, i.e. when caam knows whose credential
it is. A live credential caam cannot match to a vault profile is left alone:
the login will replace it, which is what the user asked for, but clearing it
first would lose an Account for good. A failed or cancelled login leaves the
agent logged out; the dashboard says so and that Enter on the previous
Account restores it.

Never run an agent's native login while a vaulted Account's live credential
is on disk. This applies to any agent whose login is a logout first; Codex
is the one proven to do it.

## 3. What each agent's credential actually is

The README's per-tool sections carry the user-facing facts; these are the
ones that shaped the code.

- **Claude Code (macOS).** The OAuth blob lives in the login keychain
  (service `Claude Code-credentials`); `~/.claude/.credentials.json` is
  caam's 0600 mirror of it. Capture reads the item, Switch writes it back,
  Clear removes it (`internal/authfile/keychain.go`). A profile captured
  before the bridge, or from a logged-out state, holds settings and no
  credential; `Restore` refuses it (`credentialLessProfileError`) and the
  dashboard lists it as `No credential`, because installing it would change
  nothing while reporting success. Claude Code refreshes its own token; caam
  cannot (`refresh.refreshClaude` is deliberately unsupported).
- **Codex.** `$CODEX_HOME/auth.json`, file store enforced. Tokens are JWTs
  carrying `chatgpt_account_id` and the plan. A running `codex app-server`
  caches the file at start; `--reload-daemon` SIGTERMs it after a Switch.
  Login revokes the session it finds (§2). caam can refresh a Codex token
  (`refresh.refreshCodex`) but only spends the refresh token for a reason (§6).
- **Antigravity (agy).** Closed Go binary talking gRPC; on macOS the token is
  a keychain item (service `gemini`, account `antigravity`) mirrored onto
  `~/.gemini/antigravity-cli/antigravity-oauth-token`, on Linux the file is
  the credential. The access token lives an hour and agy renews it only when
  it runs (`agy -p "…"` is enough); caam does not refresh it. No agy file
  names the signed-in Google account, so Capture asks Google's userinfo
  endpoint once and records the email in `meta.json`. Profile detection
  hashes the refresh token, so hourly access-token rotation does not lose the
  Active Account.
- **Kimi Code.** `~/.kimi-code/credentials/kimi-code.json`, plain tokens;
  `kimi login` is a device-code flow (plain `kimi` is the chat REPL and does
  not log in). `/logout` leaves the file with empty tokens, which caam treats
  as logged out. Identity from Kimi's `/me`.
- **zcode.** `~/.zcode/v2/credentials.json`, a record sealed with AES-GCM
  under a per-user secret; caam moves it verbatim and unseals it only to read
  identity and present the session token to the billing API. Re-sealed with a
  fresh IV on every write, so detection hashes the sealed user id.
- **OpenCode.** Logins live in tables of `opencode.db` next to session
  history; caam exports and restores the login rows and never swaps the
  database. The Console login exposes no usage API (`no limits API`); a Zen
  API key does.

## 4. Limits: what is fetched, from where, how often

`internal/usage` has one fetcher per agent producing a `UsageInfo`: a
primary Window (five-hour where the agent has one), a secondary (weekly), and
per-model Windows. `usage.WindowsOf` orders them for display: duration-named
windows first (5-hour, daily, weekly, monthly), then the primary/secondary
picks of a per-model agent, then the remaining models by name, each once.
Neither Anthropic nor OpenAI publishes a monthly cap; nothing is invented for
it.

- **Credential resolution** (`cmd/caam/cmd/tui.go: fetchProfileLimits`,
  shared with `caam limits`): the Active Account's limits come from its
  **live** credential, because the agent rotates that one in place and the
  vault copy is stale for exactly that Account; every other Account's come
  from its vault copy. A fetch presents the access token and nothing more; it
  never refreshes or rewrites a credential, so polling cannot replay a family.
- **Antigravity** needs two calls: `v1internal:loadCodeAssist` with the
  Antigravity client metadata (`ideType: ANTIGRAVITY`, `pluginType: GEMINI`)
  returns the account's Code Assist project, and `v1internal:retrieveUserQuota`
  must name that project. Without it Google answers `403 PERMISSION_DENIED`
  "no valid license (#3501)" even for an account in good standing. Google
  keys `loadCodeAssist` on the User-Agent: under any value but `antigravity`
  it answers as if the account were never onboarded (no project, no tier).
  The project is cached per access token. Google's opaque `chat_NNNNN`
  buckets are not models and are dropped.
- **Kimi** calls `/coding/v1/usages` with the CLI's device-identity headers
  (device id read from `~/.kimi-code/device_id`, never created). **zcode**
  calls the billing API; an account without a coding plan is reported as
  exactly that, not as 0% used.
- **Caching in the dashboard** (`internal/tui/limits.go`): one entry per
  Account, fetched for the Accounts on screen — the selected agent's rows and
  every agent's Active Account — at most once a minute each, **failures
  included** (a 429 from Anthropic once came from the dashboard's own
  relaunches). A failed fetch keeps the last known figures, marked stale, and
  the expansion says why in a short form (`auth expired (re-login)`,
  `no limits API`, `no coding plan`, `quota API refused (403)`). Keys typed
  into search or a dialog never start a fetch.

## 5. The dashboard

`caam` with no arguments (`internal/tui`), the product view. Its shape was
chosen on a design canvas ("Direction A"): a strip of agents across the top,
the selected agent's Accounts below.

- **Strip** (`internal/tui/vertical.go`): a real tab strip — a fixed slot per
  agent in key order, one horizontally scrolled row, `‹ n` / `n ›` counts for
  what is off either edge, and the window moves only when the selection would
  leave it, so nothing shifts under the cursor. Only agents with a captured
  Account are on it; there is nothing to switch between on the others, and
  `n` is how they get their first Account. An empty vault shows how to get
  started. Three tiers by width (cards ≥150 columns, chips ≥100, tabs below)
  and by height; a short terminal turns cards into tabs so the accounts pane
  keeps its rows.
- **Accounts pane**: one row per Account, `●` on the Active one, a column per
  Window; columns the pane cannot fit are dropped least-important first (LAST
  USED, then the rightmost windows) and every dropped Window is listed in the
  expansion instead, so nothing the API reported is unreachable. The selected
  Account expands in place like a tree node: the outcome of the last action
  on it, its windows without a column, auth/plan/health/token, its vault
  path, and the keys. **LAST USED** is the activity log's last activate,
  deactivate, switch or login of the Account (`db.LastUsed`); a Login from
  the dashboard is logged as one. Upstream's isolated-profile store, which
  nothing writes, is consulted first and is always empty for vault profiles.
- **Keys**: ←/→ agent, ↑/↓ Account, Enter switch (confirm), `n` new Login
  (picker of every agent, install status shown), `r` refresh (limits; the
  token first only when it has expired or the provider just refused it, and
  only for Codex and Gemini), `i` full card, `/` search, `e` edit, `o`
  browser, `d` delete, `?` help. Upstream's `b` (backup under a typed name)
  and `l` (token refresh labelled "login") were removed: Enter and `n`
  re-capture on the way through, and one `r` is easier to hold than two
  refreshes. The name dialog survives only as the `n` fallback when a login
  leaves no identity.
- **Hooks** (`internal/tui/limits.go: Hooks`): the TUI does not open
  credentials itself. The command layer supplies `Switch` (the shared
  `switchProfile`), `Limits`, `Health` (computed as `caam ls` does, not read
  from a stale snapshot), `Login` (the agent's native command plus a hint),
  `LiveIdentity` and `Capture`. Tests substitute fakes.
- **Messages**: a status-bar line belongs to the agent it was written under
  and is dropped when the focus moves (agent *or* Account; two agents with
  no Accounts have the same empty selection key, which is how a Grok error
  once followed the user to every tab). The outcome of a switch, capture or
  refusal is also the first line of the Account's expansion, not only a
  status-bar message.

`caam monitor` is the second view: the same Windows as columns across every
agent's Accounts, `*` on the Active one, Enter switches through the same
path, rows whose fetch fails keep their last good numbers. Piped or with
`--once` it prints the plain table it always did.

## 6. Refreshing a token is spending it

`caam refresh` and the dashboard's `r` use `internal/refresh`, which for
Codex and Gemini presents the vault copy's refresh token to get a fresh
access token and stores the new family in the vault (and, if the Account is
Active and the live file has not moved, restores it to the live file too).
Every such refresh consumes the refresh token. So the dashboard refreshes a
token only for a reason — the health record says it has expired, or the last
limits fetch was refused as unauthorized — and never on a plain keypress.
Claude Code, Kimi, Antigravity, zcode and OpenCode renew their own; a
refresh cannot revive a revoked family (§2).

## 7. Verifying changes

Unit tests run under `testutil.IsolatedMain` with a fake keychain; the
dashboard tests render `Model.View()` at real sizes (159×42 is Chris's
terminal) and assert on the stripped text. Visual changes are also driven
against the real binary through a pty with `pyte` (see the scratchpad
scripts referenced in the branch history: start, send keys, dump rows), and
against real accounts with `caam limits <agent>`; the two probes that
settled the Antigravity and Codex questions were plain HTTP calls with the
stored tokens, printing status and a body snippet and never the token.
`make lint` is broken on `main` and on this branch alike (golangci-lint 2.x
against a v1 config); `go vet`, `gofmt` and `go test -race` are the checks
that run.

## 8. Open items

- The five Codex Accounts revoked before §2 was understood need one `n`
  login each; they are filed under the same names.
- `.golangci.yml` needs migrating to the v2 config format.
- Which "pro" model becomes Antigravity's primary Window is decided by usage
  then name, so with everything untouched it is `gemini-2.5-pro` rather than
  a 3.x model.
- The parent handoff (`caam-handoff.md`) still describes the mission in its
  pre-product framing; ADR-0001 says it should be rewritten.
- `docs/SURFACE_AUDIT_2026-09-13.md` lists the rest of caam's surface that
  does not yet follow §1–§6: eight other switch paths that restore without
  re-capturing, two timers that spend refresh tokens, `caam add` and the
  smart handoff running a login in the unsafe order, and output surfaces
  that still say "used %" and raw ids. None of it is fixed yet.
