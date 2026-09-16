> Absorbed on 2026-09-15: the product section lives in `../ACCOUNT_SWITCHER.md`
> and `../PR_DESCRIPTION.md`, the rules in `../ACCOUNT_SWITCHER.md` §1–§6 and
> `../../AGENTS.md`, the working rules in `../../AGENTS.md` ("Working on the
> account-switcher branch"), the remaining work in `../SURFACE_PLAN.md` R6, and
> the index of documents in `../README.md`. Kept as history; the paths inside
> are as they were on the day it was written.

# Handoff: the coding-agent account switcher on the caam fork

Read `specs/CONTEXT.md` (the glossary) and `specs/adr/0001-build-account-switcher-on-caam-fork.md`
first; their terms are used below without redefinition: Agent, Account, Active
Account, Capture, Switch, Login, Limits, Window. The earlier handoffs,
`caam-handoff-v1-superseded.md` and `caam-handoff-v2-superseded.md`, are history:
v1 framed the work as "make backup/activate/limits work on this Mac", v2 as a work
list of adapters and bugs. Both are done. Read them only for what they preserve
verbatim (the Antigravity quota call, the machine survey of 2026-09-13).

## The product

One tool that lets Chris log in once with every Account on every Agent, see each
Account's Limits with each Agent's Active Account marked, and Switch any Agent to any
captured Account without a new Login. It is caam's own promise ("log in once… switch
instantly… no browser, no OAuth dance") made true on a keychain-only Mac and for six
Agents: **Codex CLI, Claude Code, Antigravity CLI, Kimi Code, zcode, OpenCode.**
Everything else caam does stays as it was.

The surfaces, all of which exist and work today:

- `caam monitor` — the dashboard. One card per Agent that has Accounts, every Account's
  Windows as "left, resets at", `*` on the Active Account. Enter switches, `n` logs a
  new Account in, `r` refreshes (token first when expired or refused, else it offers the
  Login), `d` deletes. Every question and outcome is a dialog in the middle of the
  screen. Keys and why: `specs/adr/0004-dashboard-strip-and-keys.md`.
- `caam limits` / `caam status` — the same facts as text and `--format json`.
- `caam activate <agent> <account>` — a Switch. `caam add <agent>` — a Login.
  `caam refresh` — a token refresh, only when expired or refused.
- `caam run`, the auth coordinator and `caam wezterm switch-all` — when a running
  session hits a rate limit, Switch under it and resume it on its history rather than
  log in again.

## Where the knowledge lives

`specs/architecture/ACCOUNT_SWITCHER.md` in the repo is the architecture and the lessons: why every
Login is a logout first (§2, and ADR-0002), what each Agent's credential actually is
(§3), where Limits come from (§4), the dashboard (§5), why refreshing a token is
spending it (§6), how to verify a change (§7), and the open items (§8). It is kept
current; when a fact here disagrees with it, that document wins.

`specs/plans/SURFACE_AUDIT_2026-09-13.md` and `specs/plans/SURFACE_PLAN.md` record the audit of
the whole surface against the product and the plan that closed the gaps, round by
round, each slice as a red test first. `CHANGELOG.md` (Unreleased) is the summary a
reader wants first. The ADRs under `specs/adr/` hold the decisions.

## The rules that shape the code

1. **A Switch re-Captures the outgoing Active Account before installing the
   incoming one.** Codex, Claude and Google OAuth rotate refresh-token families;
   a stale copy replayed later revokes the whole family. There is one Switch path,
   `switcher.Switch`, and every surface goes through it.
2. **A Login is a logout first.** `codex login` revokes the live session before it
   opens the browser, and the others clear theirs. So every Login is Capture → clear →
   Login, through `switcher.PrepareLogin`/`FinishLogin`, and nothing ever injects
   `/login` into a session that has another Account to switch to.
3. **Refreshing a token spends it.** `refresh.NeedsRefresh` (expired, or just refused)
   is the only gate; no timer refreshes anything; `--force` is refused.
4. **Limits are fetched when the user asks.** No background daemon fetches them.
5. **Loud failure.** A Required credential that cannot be obtained is an error, never
   an exit 0 that vaulted nothing. That bug started this work.
6. **Nothing is removed from caam and `LICENSE` is untouched** (MIT with the
   OpenAI/Anthropic rider, sha256 prefix `32a82e0a5754`). The fork adds and fixes.

## Working rules

- Repository: `/Users/csells/code/forks/Dicklesworthstone/coding_agent_account_manager`,
  branch `account-switcher`, pushed to `origin` (`csells/…`) only. Never push `main` or
  `master`, never touch `upstream`, never open a PR; whether to upstream is Chris's call.
- From `AGENTS.md`, in full: never delete a file or directory without express written
  permission, including ones you created; never `git reset --hard`, `git clean`, or
  `rm -rf`; no secrets in logs, fixtures, commits or JSON; commits carry no AI
  attribution.
- Live logins are in daily use. Read them freely; write to `~/.codex/auth.json`, the
  login keychain, `~/.kimi-code/credentials/`, `~/.zcode/v2/credentials.json` or
  OpenCode's store only through the switcher's own paths.
- Red-green: a failing test before each change. Tests never touch the real keychain
  (`testutil.FakeKeychain`) and every test package uses `testutil.IsolatedMain`.
- Checks: `make build`, `go vet ./...`, `gofmt`, `go test ./...`, `go test -race`,
  `make lint` (v2 config, clean; keep it that way).
- Install: `make build && cp caam ~/.local/bin/caam.new && mv -f ~/.local/bin/caam.new ~/.local/bin/caam`.
- Vocabulary in anything a person reads: agent, account, "left, resets at".
  Flag and subcommand names (`--tool`, `provider`) are the CLI's contract and stay.
- Never answer your own questions; leave a decision pending and do the independent
  work.

## What needs Chris

- Five Codex Accounts were revoked before rule 2 was understood. Each needs one `n`
  Login in the dashboard (`r` on the dead row offers it). Then the real round trip:
  `caam activate codex` to the least important Account and back, `limits codex`
  healthy for both, vault copies rotated.
- A Kimi rate-limit sample from a real session, so the coordinator can learn its
  pattern (decided 2026-09-14: not guessed from docs).
- The license rider (whether Gas City acts for OpenAI/Anthropic) is a business
  question. A `--warm` background refresh is deferred. Thresholds and notifications
  are not a requirement.

## Done means

Every Account Chris has, on every Agent, captured and switchable from the dashboard
and the CLI, with truthful Limits, on this machine. Not a green test on one Account per
Agent. The acceptance block in `specs/architecture/ACCOUNT_SWITCHER.md` §7 is the check.
