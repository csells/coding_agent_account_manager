# Surface audit, 2026-09-13: the rest of caam against the switcher's rules

A read-only audit of caam's CLI, output formats, HTTP API and the parts of the
TUI the account-switcher work did not touch, checked against the principles in
`ACCOUNT_SWITCHER.md` and the credential rules in `AGENTS.md`. Every
file:line was read at the time; the five most consequential (1, 2, 3, 6, 9)
were re-verified by hand. Status (2026-09-14): findings 1–15 (credential
paths) and 26–32 (the dashboard's edges) are fixed on the `account-switcher`
branch per `SURFACE_PLAN.md` gaps 1, 2, 3 and 5; findings 16–25 (output
formats) are fixed as gap 4. A review of the whole change (19 findings)
was acted on the same night: one shared capture for logins that also
handles system-profile matches, clearing that respects files shared
between tools, refresh moved into the switch core behind the one gate,
terminal pool refusals, login logging through the core, and the dead
threshold and handoff-login plumbing removed. Left as noted in the gap 4
report: four stale "valid providers: codex, claude, gemini" hints in
`robot.go` outside the fixed commands, and `limits --best`/`--recommend`
still say "used".

Rule numbers refer to `AGENTS.md` "Credentials — the rules the switcher lives
by": (1) re-capture before you replace, (2) a login is a logout first,
(3) refreshing a token spends it, (4) a revoked family cannot be revived,
(5) live stores change only through caam's own paths.

## A. Credential paths (most important)

1. **`caam run --precheck` switches with a bare `vault.Restore`.**
   `cmd/caam/cmd/run.go:385-393`: the selector picks a better profile and
   `Restore`s it; no `ResnapshotOutgoing`, no `performSwitch`, though the
   current profile is known. The help at `run.go:60` recommends
   `alias claude='caam run claude --precheck --'`, i.e. every invocation.
   `run.go:234-237` (no active profile) restores with no backup of the live
   credential. Rule 1, the #19 sequence. Fix: both call
   `switchProfile(ctx, tool, sel, switchOptions{Quiet: quiet})`. S.
2. **Smart handoff restores a vaulted credential, then runs the native login
   on top of it.** `internal/exec/smart_runner.go:427-441`: re-snapshot is
   warn-only, `Restore(nextProfile)`, then `loginHandler.TriggerLogin`, which
   for Codex injects `codex login` (`internal/handoff/codex.go:26-28`) — the
   login that revokes the family caam just installed. Guarded only by
   `AutoTrigger: false` (`internal/config/spm_config.go:370-377`). Rules 1
   and 2. Fix: drop the login step (a restored credential is logged in), or
   make it the capture→clear→login sequence with no restore first; make the
   re-capture abort. M.
3. **`caam daemon` refreshes every profile's token on a five-minute timer.**
   `internal/daemon/daemon.go:24-27, 657-671`: `refresh.ShouldRefresh` then
   `refresh.RefreshProfile`. Rule 3. Fix: delete the timer refresh; keep the
   daemon's vault-backup scheduler. M.
4. **`caam pool` / `daemon --pool` is a second timer refresher that retries
   failures forever.** `internal/authpool/monitor.go:160-178`: a refused
   refresh is re-presented every minute, exactly what trips reuse detection;
   `pool.go:151-157 --all` calls `RefreshAll`. Rule 3. Fix: a refused refresh
   is terminal until a human acts; remove the timer; one implementation. M.
5. **`refresh.ShouldRefresh` is the inverse of the dashboard's rule.**
   `internal/refresh/refresh.go:26-37` returns `ttl > 0 && ttl < threshold`:
   refreshes a still-valid token, refuses an expired one. The dashboard's
   `tokenNeedsRefresh` refreshes only when expired or refused; `caam refresh`
   has a third gate. `performSwitch`'s `refreshIfNeeded`
   (`activate.go:630-645`) spends a token ten minutes early. Fix: one exported
   gate, expired-or-refused, used everywhere. S.
6. **HTTP API `POST /api/v1/actions/activate` is a bare Restore.**
   `internal/api/handlers.go:386-388`. Rule 1. Fix: lift `performSwitch`
   into `internal/switcher` that both cmd and api call. M (pays for 1, 7, 8).
7. **`caam next` warns instead of aborting.** `next.go:98-105, 219-228`:
   a failed re-snapshot prints a warning (nothing under `--quiet`) and
   restores anyway. Fix: call `switchProfile` with next's `--force`. S.
8. **Three more bare-Restore switch sites.** `workspace.go:310-320`,
   `robot.go:848-853` (the agent-facing JSON API reads the active profile and
   restores without re-capturing it), `internal/wrap/wrap.go:332-335`
   (restores on every retry). Fix: route through the shared core. S each.
9. **`caam add` is an incomplete duplicate of the dashboard's `n`.**
   `add.go:106-113` vaults the outgoing credential as `_auto_backup_<ts>`,
   not under its own account, so that account's copy stays stale;
   `:115-123` clears with raw `os.Remove` rather than
   `authfile.ClearAuthFiles`, which is what scrubs the Claude Desktop
   config, empties OpenCode's tables and clears the keychain item (issue
   #98) — so on macOS `caam add claude` is not a logout first; `:175-183`
   asks for a name instead of reading identity. Fix: make `runAdd` a
   non-interactive wrapper over the same steps as `n`. M.
10. **`ResnapshotOutgoing`'s doc comment says the opposite of rule 1.**
    `authfile.go:863-865`: "intended to be treated as NON-FATAL (a failed
    re-snapshot must never block a switch)". `performSwitch` does the
    opposite with a long rationale; the comment is the likely reason
    `next.go` and `smart_runner.go` warn and proceed. Fix the comment. S.
11. **`auth-coordinator` and `wezterm login-all` inject `/login` into live
    sessions with no capture and no clear.** `coordinator.go:441`,
    `wezterm.go:228`. Rule 2 (proven for Codex, unproven for Claude's
    `/login`). Fix: capture first, refuse on failure, name the risk. M.
12. **`caam login claude <profile>` (isolated) likely writes the shared
    keychain.** `internal/provider/claude/claude.go:374-410, 568-571`: the
    login keychain is per OS user, not per HOME, so the `/login` replaces the
    live Claude credential with no capture and no clear; same for agy.
    Codex isolation is fine (`CODEX_HOME`). Not proven live. Fix: refuse on
    darwin for keychain-backed providers, or run the rule-2 preamble. M.
13. **The TUI keeps an unsafe fallback switch.** `internal/tui/limits.go:
    218-222`: with no `Switch` hook, `switchCmd` calls `doActivateProfile`, a
    bare Restore (`model.go:1717-1719`). Unreachable under `caam` but live for
    any other embedder and for tests. Fix: delete it; say "switching
    unavailable". S.
14. **`caam watch` files `auto-<timestamp>` profiles.**
    `internal/discovery/watcher.go:346-351`: each is another copy of the same
    rotating family; restoring any but the newest replays a consumed token.
    Fix: never auto-file; log and let the user name it. S.
15. **`caam refresh --all --force` spends every vaulted refresh token in one
    command.** `refresh.go:294-296`. Fix: reject the combination; one help
    line saying a refresh consumes the refresh token. S.

## B. Output formats

16. **`caam limits` table is the inverse of the dashboard.** `limits.go:513`
    header `PRIMARY SECONDARY SCOPED RESETS IN`; `:553-556, 600-608` print
    `UsedPercent` as a bare `NN%` and the reset as a duration. The dashboard
    says `88% left · 8:50 PM`; `limits` says `12%`. `--rank` and `--forecast`
    likewise. Fix: render through `usage.WindowsOf` + `WindowLeftText`, one
    column per window; leave `--format json` alone. M.
17. **`limits --format json` is consistent**: it marshals `[]usage.ProfileUsage`
    unchanged (`used_percent`, `resets_at`). Nothing in the repo mentions
    vibe_cockpit. One README line should say JSON is used-side by contract
    while tables are left-side.
18. **`caam ls`: random provider order, raw ids, no LAST USED.**
    `root.go:1399` ranges a map; `:1404` prints the raw id as the header.
    `db.LastUsed` already returns everything in one query. Fix: strip order,
    `providerLabel`, LAST USED column and `last_used` in JSON. M.
19. **`caam status` lists never-logged-in tools and raw ids.**
    `root.go:983-999, 1044`. The dashboard hides exactly those. Fix:
    `providerLabel`; not-logged-in tools on a one-line footer. S.
20. **`caam which` knows three providers.** `defaults.go:95`
    `[]string{"codex","claude","gemini"}`; a default on agy/kimi/zcode/opencode
    is invisible. Fix: iterate the registered tools. S.
21. **`caam robot limits` fetches no limits; `robot next`'s LRU is a stub.**
    `robot.go:1245-1251` promises usage and reset times; `:1429-1447` derives
    an availability score from health only. `:751-757` "Could check last used
    time here". Fix: delegate to `fetchProfileLimits`, emit `left_percent` and
    `resets_at`, use `db.LastUsed`. M.
22. **API `/usage` reports the health-probe time as `last_used`.**
    `handlers.go:89, 336-338`. Fix: `db.LastUsed`, or rename to
    `last_checked`. S.
23. **`caam monitor` non-interactive output: hardcoded provider list and
    used%.** `render_brief.go:27` lists five providers so agy/kimi/zcode/grok
    never appear in `--format brief`; `render_table.go:51` uppercases the raw
    id; `:67-76` and `render_alert.go:55` print used% with no polarity word.
    Fix: build the list from state; `providerLabel`; `WindowLeftText`. S–M.
24. **The `i` card contradicts the LAST USED column.** `model.go:2506` reads
    only the isolated store; the row falls back to the activity log. Fix: same
    fallback in `buildDetailInfo`. S.
25. **Three display-name vocabularies.** `provider_panel.go:63-77`
    `providerLabel`; `provider.go:205-253` `ProviderMeta.DisplayName`
    ("Antigravity (Google)"); `auth.go:301-306`. Fix: one exported
    `provider.Label(id)` used by TUI, monitor, ls, status, which. M.

## C. The untouched TUI

26. **In-app help still recommends the manual ritual `n` replaced.**
    `help_renderer.go:284-289`: "backup → clear → /login → backup → activate",
    and "Email/account ID not available in current Claude auth format". S.
27. **Help says `E` exports an "encrypted bundle"; it does not.**
    `help_renderer.go:234` vs `export_import.go:108-117, 141`,
    `internal/bundle/export.go:59-67`: no encryption, output dir is the cwd,
    the dialog names neither. Fix wording and dialog, or prompt for a
    passphrase. S / M.
28. **Empty-state text points at the CLI where the pane above says press
    `n`.** `profiles_panel.go:352-355` "Run: caam backup …" (also
    `vertical.go:844, 471`, `model.go:1362`, `detail_panel.go:288`). S.
29. **The `n` naming fallback reports itself as a backup.**
    `model.go:1623-1645` "Backed up %s auth to '%s'", fields named
    `backupDialog`/`backupProvider`. Fix the wording; rename. M (tests).
30. **Keys and palette drift.** `keys.go:50-57` Left binds `h`, Right no
    longer binds `l`; `help_renderer.go:208-241` omits `i` and `ctrl+p`;
    `dialog.go:858-872` `DefaultCommands` has no New Login / Full Card /
    Search and says "Profile"; `model.go:1102-1115` the card closes on
    `keys.Cancel`, which includes `n`. S.
31. **README and AGENTS contradictions.** `README.md:1418` "Proactive Token
    Refresh — automatically refreshes OAuth tokens before they expire"
    contradicts rule 3; `README.md:1203, 1220` document `show_key_hints` /
    `CAAM_TUI_KEY_HINTS` but `model.go:3005-3006` renders hints
    unconditionally; `AGENTS.md:350` describes the TUI as "profile list,
    status, rotation controls"; `init.go:136-138` onboarding never mentions
    bare `caam`. S each.
32. **Minor.** Sync panel `s` copies vault credentials over SSH from a bare
    keypress with no confirm (`model.go:1607-1611`). The doc says "agent",
    the strip title and key help say "provider" (`vertical.go:379`,
    `keys.go:52-60`).

## D. Spirit verdicts

- **Dangerous under rule 3**: daemon timer refresh, pool monitor,
  `refresh --all --force`.
- **Dangerous under rule 1, and redundant with `performSwitch`**:
  `run --precheck`, `next`, `workspace`, `robot act`, `wrap`, API activate,
  the TUI fallback.
- **Dangerous under rule 2**: smart-handoff auto-login, coordinator `/login`
  injection, wezterm login-all; `caam login` for claude/agy on macOS.
- **Redundant with `n`, and less safe**: `caam add`.
- **Redundant with re-capture, with a dangerous edge**: `watch` auto-capture.
- **Consistent**: `performSwitch`/`switchProfile` and everything that calls
  it (dashboard Enter, monitor Enter, `pick`); the `n` flow and
  `ClearAuthFiles`; the `r` gate; `fetchProfileLimits`; `caam clear`;
  `limits --format json`; `caam use/resume/exec/shell`; auth-agent browser
  automation; `internal/sync` defaulting rotating providers to host-local.

## Suggested order

1. Findings 6 and 10 first: one shared switch core in `internal/switcher`,
   comment corrected. Then 1, 7, 8, 13 become one-line call replacements.
2. Findings 3, 4, 5, 15: one refresh gate, no timers.
3. Findings 2, 9, 11, 12, 14: every login path follows capture → clear →
   login, or is removed.
4. Findings 16, 18, 19, 20, 23, 25: one vocabulary and "left, resets at"
   framing everywhere a human reads it.
5. The rest.
