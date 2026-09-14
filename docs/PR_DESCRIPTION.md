# Account switcher

## Summary

This branch turns caam into an account switcher: log in once with every
account on every coding agent, see each account's rate-limit windows, and
switch without logging in again. It adds three agents, Antigravity quota,
two keychain bridges, a redesigned TUI and an interactive `caam monitor`.
Under all of it are three rules about rotating OAuth credentials, each
learned against a real account. The design document is
`docs/ACCOUNT_SWITCHER.md`; this description leads with the ideas.

## The one rule everything follows from

A credential in the vault is a copy of a session, not a password. For
Claude Code, Codex and Antigravity that session is a rotating refresh-token
family: every refresh consumes the refresh token and issues a new one, and
presenting a consumed token revokes the whole family (issue #19). So the
vault copy of the active account goes stale while the agent runs, and a
rotated-past copy can never be revived.

Therefore every switch re-captures the outgoing account first and aborts if
it cannot (`--force` overrides). That lives in one place,
`internal/switcher.Switch`, called by `caam activate`, `next`, `run`
(including `--precheck`), `workspace`, `robot act`, the wrap retry loop,
the HTTP API, the TUI and `monitor`.

## A login is a logout first

`codex login` (0.154) runs `logout_with_revoke` on whatever session it finds
in `auth.json` before starting the new login. That revokes the family and
kills a vault copy taken seconds earlier; nothing brings the account back.
So every login is capture, clear, then log in: `internal/switcher.Login`
re-captures the active account, clears the live credential so the native
login has nothing to revoke, runs the login, and captures under the
identity that signed in. The dashboard's `n` and `caam add` share it.

## Refreshing spends a token

Every refresh consumes the refresh token, so there is one gate,
`refresh.NeedsRefresh`: expired, or just refused by the service. Never
early, never on a timer. The threshold gate (`ShouldRefresh`, which
refreshed valid tokens and refused expired ones) and its config getter are
gone, the daemon's five-minute refresh is gone, the pool monitor no longer
retries a refused refresh, and `caam refresh --all --force` is refused.

## Switch, then resume

A running session holds its credential in memory, so switching the file
under it is not enough, and injecting `/login` is a new OAuth login. The
smart handoff, the coordinator and the new `caam wezterm switch-all`
switch through the core, end the session and resume it on its history
(`claude --continue`, `codex resume --last`, `gemini --resume latest`,
`kimi --continue`). Only with no other vaulted account does the
coordinator fall back to `/login`, capturing first.

## The dashboard

`caam` with no arguments: a strip of agents across the top, the selected
agent's accounts below, two columns per window: what is left (`88% left`)
under the window's name and the clock it resets at under `RESETS`. Enter
switches, `n` logs a new account in, `r` refreshes limits (and the token
only when the gate says so); every question and outcome is a dialog.
Limits are fetched at most once a minute per account, failures included.

## New adapters and keychain bridges

Kimi Code (`kimi login` device flow), zcode (an AES-GCM sealed credential
moved verbatim, unsealed only to read identity and query billing) and
OpenCode (login rows exported from and restored into `opencode.db`; the
database is never swapped). On macOS, Claude Code's and Antigravity's
credentials are keychain items; capture, switch and clear go through the
item. A profile with settings but no credential is listed as
`No credential` and refused.

## The Antigravity quota discovery

`retrieveUserQuota` answers `403 PERMISSION_DENIED` "no valid license
(#3501)" for an account in good standing unless the request names the
account's Code Assist project. The project comes from
`v1internal:loadCodeAssist` with `ideType: ANTIGRAVITY`, and Google keys
that call on the User-Agent: under anything but `antigravity` it answers as
if the account were never onboarded.

## What changed for existing users

- Every switch re-captures first and aborts on failure; `--force` overrides.
- Threshold-based refresh and `GetRefreshThreshold` are removed
  (`refresh_threshold` is still accepted; nothing refreshes on it); no
  daemon timer refresh; `refresh --all --force` is refused.
- The smart handoff switches and resumes instead of injecting a login;
  `LoginHandler` lost `TriggerLogin` and gained `ResumeArgs`.
- The three-panel TUI is replaced by the dashboard; the `b` and `l` keys are
  gone (`l` now moves right); `caam add` no longer files `_auto_backup_`
  profiles.
- `caam limits` shows one `LEFT`/`RESETS` pair per window instead of used
  percentages under `PRIMARY SECONDARY SCOPED RESETS IN`; `ls`, `status`,
  `which`, `robot limits`, `monitor` and the API's `/usage` follow.
- Table headers, help and errors say "agent" and product names; the HTTP
  API says "unknown agent".
- Isolated `caam login` for Claude Code and Antigravity is refused on
  macOS; `caam watch` no longer files auto-named profiles.

## What did not change

`limits --format json` is pinned byte for byte by a golden test. Flag
names, subcommand names and JSON keys keep their contract. The Gemini, Grok
and Cursor adapters are untouched. LICENSE is unchanged.

## How it was verified

`go test -race ./...`, `go vet`, `gofmt` and `make lint` (golangci-lint v2
config; CI installs v2) are clean. The credential rules were
checked against real accounts on every agent: switch round trips, `caam
limits <agent>` for each, and plain HTTP probes with stored tokens that
settled the Codex revocation and Antigravity project questions.
