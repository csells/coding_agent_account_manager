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
  failed re-capture aborts the operation (`internal/switcher.Switch`, built on
  `authfile.Vault.ResnapshotOutgoing`; `Force` to override). Every path that
  switches — `caam activate`, `next`, `run`, `workspace`, `robot act`, the
  wrap retry loop, the HTTP API, the dashboard's Enter, `caam monitor`'s
  Enter — calls that one core, so this cannot be forgotten in one of them.
  `performSwitch` in `cmd/caam/cmd/activate.go` is only the interactive
  wrapper (printing, the stealth delay, the Codex daemon reload).
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

The rule, implemented once in `internal/switcher` (`PrepareLogin`, `Run`,
`FinishLogin`; `Login` for the three in order) and used by the dashboard's
`n` and by `caam add`:

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

Step 2 runs only when step 1 succeeded. A live credential caam cannot match to
a vault profile (or that matches only an immutable system profile) is
somebody's session too: `CaptureSignedIn` files it as a fresh `_backup_`
first, then it is cleared — never destroyed, never left for the login to
revoke. A failed or cancelled login leaves the
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
  token first only when it has expired or the agent's service just refused it, and
  only for Codex, Gemini and Kimi; when a refresh cannot help — session ended, or
  an agent that renews its own tokens — `r` offers the Login instead and
  yes runs the `n` flow for that agent), `i` full card, `/` search, `e`
  edit, `o` browser, `d` delete, `?` help.
- **Questions and outcomes are dialogs.** Every question the dashboard
  asks — switch to this account, delete it, log in again, which agent —
  is a dialog in the middle of the screen (`ConfirmDialog` via
  `openConfirm`, the agent picker), never a `(y/n)` on the status bar.
  Everything that answers an action — switched, logged in, refused, failed,
  deleted, refreshed — opens a message dialog there too (`MessageDialog`,
  via `showMessage`), one key to dismiss. The status bar carries progress ("Refreshing limits…",
  "Login finished; reading who signed in…") and nothing the user must not
  miss. The expansion under the Account keeps the last outcome as its first
  line for context after the dialog is gone. The name dialog exists only as
  the `n` fallback when a login leaves no identity.
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

`caam refresh`, `caam activate` and the dashboard's `r` use
`internal/refresh`, which for Codex, Gemini and Kimi presents the vault
copy's refresh token to get a fresh access token and stores the new family
in the vault (and, if the Account is Active and the live file has not
moved, restores it to the live file too). Kimi's refresh is the CLI's own
call — a form-encoded `POST {oauthHost}/api/oauth/token` with the CLI's
client id and `X-Msh-*` device headers, `oauthHost` following
`KIMI_CODE_OAUTH_HOST` then `KIMI_OAUTH_HOST` — and 401, 403 or
`invalid_grant` from it is the session-gone class that offers a login. Every such refresh consumes the refresh
token. So there is one gate, `refresh.NeedsRefresh`: expired, or just
refused by the provider — never early, never on a timer, never on a plain
keypress. The daemon keeps the vault-backup schedule and does not refresh;
the pool monitor tends cooldowns on its tick, and its explicit `RefreshAll`
takes expired profiles only — a refused refresh is terminal until a person
acts. `caam refresh --all --force` is refused. Claude Code, Antigravity,
zcode and OpenCode renew their own; a refresh cannot revive a revoked
family (§2).

## 7. Verifying changes

Unit tests run under `testutil.IsolatedMain` with a fake keychain; the
dashboard tests render `Model.View()` at real sizes (159×42 is Chris's
terminal) and assert on the stripped text. Visual changes are also driven
against the real binary through a pty with `pyte` (see the scratchpad
scripts referenced in the branch history: start, send keys, dump rows), and
against real accounts with `caam limits <agent>`; the two probes that
settled the Antigravity and Codex questions were plain HTTP calls with the
stored tokens, printing status and a body snippet and never the token.
`make lint` runs again since 2026-09-14 (`.golangci.yml` migrated to the
v2 format, findings fixed) and is clean; `go vet`, `gofmt` and
`go test -race` are the other checks.

## 8. Open items

- The five Codex Accounts revoked before §2 was understood need one `n`
  login each; they are filed under the same names.
- Which "pro" model becomes Antigravity's primary Window is decided by usage
  then name, so with everything untouched it is `gemini-2.5-pro` rather than
  a 3.x model.
- The parent handoff (`caam-handoff.md`) is now written around the product
  mission per ADR-0001 and points here; the two earlier framings are kept
  beside it as `caam-handoff-v1-superseded.md` and `-v2-superseded.md`.
- `docs/SURFACE_AUDIT_2026-09-13.md` and `docs/SURFACE_PLAN.md` record the
  gaps between the dashboard and the rest of caam and the plan that closed
  them. Gaps 1 (one switch core), 2 (one refresh gate, no timers), 3
  (every login is capture → clear → login) and 5 (the dashboard's edges)
  and 4 (one vocabulary and "left, resets at" in every output) are done,
  and so is round 2 (robot hints from the registry, Kimi refreshes its own
  token, switch-then-resume, "agent" in every string a person reads, this
  handoff). What remains waits on Chris (R6).
- A running session holds its credential in memory, so switching the
  file under it is not enough. The smart handoff (`caam run` with handoff
  enabled), the coordinator and `caam wezterm switch-all` therefore
  switch through the core, end the session and resume it on its history
  (`claude --continue`, `codex resume --last`, `gemini --resume latest`,
  `kimi --continue`). Nothing injects `/login` while another vaulted
  account exists; with none, the coordinator captures the signed-in
  account and then sends `/login`, and `wezterm login-all` does the same.
