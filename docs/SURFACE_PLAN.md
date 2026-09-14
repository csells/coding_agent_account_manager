# Plan: bring the rest of caam up to the dashboard's rules

The audit (`SURFACE_AUDIT_2026-09-13.md`) found five gaps between what the
dashboard does and what the rest of caam does. This is the plan to close
them, gap by gap, each as red → green slices: a failing test that states
the behaviour, the least code that passes it, then tidy. Test names use
the glossary in `CONTEXT.md` (Agent, Account, Capture, Switch, Login,
Limits, Window); test packages run under `testutil.IsolatedMain` with the
fake keychain and synthetic tokens, never a real credential.

Order: 1, 2, 3 are credential safety and go first, in that order; 4 and 5
are output and can run in parallel with them. Each gap ends with `go vet`,
`gofmt`, `go test -race` on the packages touched, a `code-review` pass of
the whole change, and the checks in `ACCOUNT_SWITCHER.md` §7.

Rule numbers are the credential rules in `AGENTS.md`.

## Gap 1 — one switch core (rule 1)

**Goal.** Every Switch in caam re-captures the outgoing Active Account and
aborts if it cannot, through one implementation. Today only
`performSwitch` in `cmd/caam/cmd/activate.go` does; seven other sites call
`vault.Restore` directly.

**Design.** New package `internal/switcher`:

```go
type Options struct {
    Tool, Profile, Previous, Source string
    Force, BackupCurrent bool        // Force: proceed when re-capture fails
    Config *config.SPMConfig
    DB     *caamdb.DB                // optional: activity log
    Health *health.Storage           // optional: refresh gate input
}
type Result struct {
    PreviousProfile string
    Refreshed, AutoBackup, Recaptured bool
    RecaptureWarning string
}
func Switch(ctx context.Context, vault *authfile.Vault, fileSet authfile.AuthFileSet, opts Options) (*Result, error)
```

`Switch` does what `performSwitch` does minus the interactive parts:
refresh the incoming token when the one gate says so (Gap 2), auto-backup
an unvaulted live credential, `ResnapshotOutgoing` and **abort unless
Force**, `Restore`, log the switch. Printing, the stealth delay and the
Codex daemon reload stay in `performSwitch`, which becomes a thin wrapper.
`internal/api` and `internal/wrap` can import `internal/switcher`; `cmd`
cannot be imported by them, which is why the core moves.

**Slices.**

1. `TestSwitch_RecapturesTheOutgoingAccountFirst` — a vaulted Account A
   is live and its live file has rotated; `Switch` to B leaves A's vault
   copy equal to the rotated live file, and B live. *Green:* the package,
   with `ResnapshotOutgoing` + `Restore`.
2. `TestSwitch_AbortsWhenTheOutgoingAccountCannotBeRecaptured` — make A's
   vault directory unwritable; `Switch` returns an error naming A and the
   live file is untouched. `Force: true` proceeds and reports the warning.
3. `TestSwitch_VaultsAnUnknownLiveCredentialBeforeReplacingIt` — a live
   credential matching no vault profile is filed as an auto-backup before
   B is installed (the "smart" mode today), never destroyed.
4. `TestSwitch_RefusesAnAccountWithoutACredential` — a settings-only
   profile is refused up front (`credentialLessProfileError`), live file
   untouched.
5. `TestSwitch_LogsTheSwitch` — with a DB, one `activate` event for B and,
   when A was known, a `deactivate` for A (what `logProfileSwitch` does).
6. `performSwitch` calls `switcher.Switch`; the existing
   `cmd/caam/cmd/activate_*_test.go` stay green. Delete the duplicated
   body.
7. `TestRunPrecheck_SwitchesThroughTheCore` (`cmd/caam/cmd/run_test.go`,
   new) — `--precheck` with a better profile available re-captures the
   current one; a failed re-capture leaves the current profile live.
   Same for the no-active-profile branch (`run.go:234`), which must
   auto-backup rather than clobber.
8. `TestNext_AbortsWhenRecaptureFails` — `caam next` with an unwritable
   outgoing vault dir errors instead of warning; `--force` proceeds.
   Both `next.go` sites call the core.
9. `TestWorkspaceActivate_SwitchesEachToolThroughTheCore`,
   `TestRobotAct_SwitchesThroughTheCore`,
   `TestWrapRunOnce_SwitchesThroughTheCore` — one test each at the
   command/package seam; each site becomes a call.
10. `TestAPIActivate_RecapturesBeforeRestoring` (`internal/api`) — the
    handler goes through the core; a failed re-capture is a 409-style
    error, not a switch.
11. `TestTUISwitchWithoutAHookSaysSo` — `internal/tui/limits.go` fallback
    `doActivateProfile` is deleted; with no `Switch` hook the dashboard
    reports "switching is not available" and restores nothing.
12. Rewrite the `ResnapshotOutgoing` doc comment (`authfile.go:863`): a
    failed re-capture aborts; `Force` is the only override.

**Acceptance.** `grep -rn "vault.Restore(" cmd internal --include='*.go' |
grep -v _test` lists only `internal/switcher` and the deliberate
non-switch uses (`caam add` rollback, refresh's active-file update, the
restore-original path in `uninstall`).

## Gap 2 — one refresh gate, no timers (rule 3)

**Goal.** A refresh token is spent only when the token has expired or the
provider just refused it, and only when a person asked. No timer refreshes
anything.

**Slices.**

1. `TestNeedsRefresh_OnlyWhenExpiredOrRefused` (`internal/refresh`) — new
   exported `NeedsRefresh(h *health.ProfileHealth, lastErr error) bool`:
   false for a valid token with 5 minutes left, true for an expired one,
   true for a valid one whose last fetch was `unauthorized`. `ShouldRefresh`
   is deleted; the three current gates (`refresh.go:26`, `activate.go:642`,
   `cmd/caam/cmd/refresh.go:298`, and the dashboard's `tokenInTrouble`)
   call this one.
2. `TestSwitch_DoesNotSpendARefreshTokenForAValidOne` (`internal/switcher`)
   — a switch to an Account whose token has an hour left makes no refresh
   call (fake refresher records calls).
3. `TestRefreshAll_RefusesForce` (`cmd/caam/cmd/refresh_test.go`) —
   `caam refresh --all --force` exits with a usage error and spends
   nothing; help says "a refresh consumes the refresh token".
4. `TestDaemon_NeverRefreshesOnATimer` (`internal/daemon`) — a daemon
   running two check intervals with an expiring profile makes zero refresh
   calls. The timer refresh (`daemon.go:657-671` and the pool refresher)
   is deleted; the vault-backup scheduler stays.
5. `TestPool_ARefusedRefreshIsTerminal` (`internal/authpool`) — a profile
   in `PoolStatusError` is not retried by the monitor; `monitor.go:160-178`
   loses the `Error` clause and the timer; `caam pool` keeps status/list.
   `caam daemon --pool` and `caam pool refresh --all` say what they no
   longer do.
6. README: the "Proactive Token Refresh" bullet becomes "on-demand token
   refresh, only when expired or refused"; `caam daemon --help` matches.

## Gap 3 — every Login is capture → clear → login (rule 2)

**Goal.** No path runs an Agent's native Login while a vaulted Account's
live credential is on disk, and `caam add` is the CLI form of the
dashboard's `n`.

**Design.** The four steps of `startNewAccountLogin` (capture the Active
Account or abort, `ClearAuthFiles` or abort, run the Login, read identity
and Capture) move into `internal/switcher.Login(ctx, vault, fileSet,
LoginOptions)` with an injectable runner for the login command and the
identity lookup, so the dashboard and `caam add` share it.

**Slices.**

1. `TestLogin_CapturesThenClearsThenRuns` (`internal/switcher`) — with A
   vaulted and live, `Login` leaves A's vault copy fresh, the live file
   absent when the runner starts (the fake runner asserts it), and the
   new credential captured under the identity the lookup returns.
2. `TestLogin_FilesAnUnknownLiveCredentialBeforeClearingIt` — no vault
   match: filed as a `_backup_`, then cleared; the runner finds nothing.
   (Revised from "left for the login to replace": leaving it is a rule-zero
   risk and a rule-2 breach at once.)
3. `TestLogin_AbortsWhenCaptureFails` / `..._WhenClearFails` — nothing
   runs.
4. `TestLogin_AsksForANameWhenIdentityIsUnknown` — result carries
   `NeedsName: true` and the captured-under name is empty; the dashboard
   opens its name dialog, `caam add` prompts.
5. Dashboard `startNewAccountLogin` calls `switcher.Login`; the tests in
   `newaccount_test.go` stay green.
6. `TestAdd_IsTheCLIFormOfN` (`cmd/caam/cmd/add_test.go`) — `caam add codex`
   vaults the outgoing Account under its own name (not `_auto_backup_`),
   clears through `ClearAuthFiles`, files the new Account under its
   identity. The `_auto_backup_` path and the raw `os.Remove` loop go.
7. `TestSmartHandoff_DoesNotRunALoginOnARestoredAccount`
   (`internal/exec`) — after `Restore(next)` no login is triggered; the
   handoff either switches (restored credential is logged in) or, when
   `next` has no credential, runs `switcher.Login`. `ResnapshotOutgoing`
   there aborts, not warns.
8. `TestCoordinator_CapturesBeforeInjectingLogin` and
   `TestWeztermLoginAll_CapturesBeforeInjecting` — both refuse when the
   Active Account cannot be captured, and say in the prompt that the
   Login ends the current session.
9. `TestLoginIsolated_RefusesKeychainProvidersOnDarwin`
   (`internal/provider/claude`, `agy`) — `caam login claude <profile>`
   on darwin returns an error explaining the login keychain is shared;
   Codex isolation unchanged.
10. `TestWatch_NeverFilesAnAutoNamedProfile` (`internal/discovery`) — an
    unidentified credential is logged, not vaulted as `auto-<ts>`.

## Gap 4 — one vocabulary and "left, resets at" everywhere (output)

**Goal.** Wherever a person reads caam, providers have product names and
windows say what is left and when it resets; JSON keeps its contract.

**Slices.**

1. `TestProviderLabel_IsTheOneVocabulary` (`internal/provider`) — new
   exported `provider.Label(id)`: Antigravity, Kimi Code, zcode, OpenCode,
   Grok, Claude Code, Codex, Gemini, Cursor. `internal/tui.providerLabel`,
   `ProviderMeta.DisplayName` and `auth.go`'s helper delegate to it.
2. `TestLimitsTable_SaysLeftAndResetClock` (`cmd/caam/cmd`) — `caam limits
   claude` renders one column per Window through `usage.WindowsOf` +
   `WindowLeftText` (`88% left · 8:50 PM`), provider labelled, STATUS as
   today; `--rank` and `--forecast` say "left"; `--format json` byte-for-
   byte unchanged (golden test).
3. `TestLs_IsOrderedLabelledAndDated` — `caam ls` lists providers in
   strip order, titled by label, with LAST USED from `db.LastUsed` and
   `last_used` in `--json`.
4. `TestStatus_HidesNeverLoggedInToolsAndLabelsTheRest` — never-logged-in
   tools move to one footer line; TOOL column shows labels; identity from
   `meta.json` (Antigravity, Kimi) fills EMAIL instead of `unknown`.
5. `TestWhich_KnowsEveryProvider` — `caam which` iterates the registered
   tools.
6. `TestRobotLimits_ReturnsLimits` — `robot limits` delegates to
   `fetchProfileLimits` and emits `left_percent`, `resets_at`, `last_used`;
   `robot next` uses `db.LastUsed` for LRU.
7. `TestMonitorBrief_ListsEveryProviderAndSaysLeft` — brief/table/alerts
   build the provider list from state, label it, and say "left" (or
   "used" explicitly) with a clock reset.
8. `TestAPIUsage_LastUsedIsFromTheActivityLog` — `/usage` `last_used` from
   `db.LastUsed`; the probe time is `last_checked`.
9. `TestDetailCard_LastUsedMatchesTheRow` (`internal/tui`) — the `i` card
   falls back to the activity log like the row.

## Gap 5 — the dashboard's edges (consistency)

**Slices.**

1. `TestHelp_TeachesNNotTheRitual` — `help_renderer.go` recommends `n`,
   lists `i` and `ctrl+p`, drops the "email not available" note.
2. `TestExportDialog_SaysWhereAndThatItIsPlaintext` — the `E` dialog
   names the output directory and that the bundle is not encrypted;
   help stops saying "encrypted". (Encrypting is a separate decision.)
3. `TestEmptyStates_SayPressN` — every "Run: caam backup …" empty-state
   string (`profiles_panel.go:352`, `vertical.go`, `model.go:1362`,
   `detail_panel.go:288`) says "press n to log in".
4. `TestNameDialog_SpeaksOfLogin` — the `n` naming fallback says "Logged
   in to X as Y" / "Login cancelled"; `backupDialog`/`backupProvider`
   become `nameDialog`/`nameProvider`.
5. `TestKeys_VimPairAndPalette` — Right binds `l`; `DefaultCommands` has
   New Login, Full Card, Search and says "account"; the `i` card closes on
   esc only, so `n` over it opens the picker.
6. `TestSyncSendAsksFirst` — the sync panel's `s` confirms in a dialog
   before copying credentials over SSH.
7. README: drop the `show_key_hints` setting or honour it; `init`
   onboarding mentions bare `caam`; AGENTS.md's TUI row describes the
   strip.

## Closing

- Update `ACCOUNT_SWITCHER.md` §1–§6 where the code moved (the switch core
  and login live in `internal/switcher`), `CHANGELOG.md` Unreleased, and
  mark each audit finding in `SURFACE_AUDIT_2026-09-13.md` as done with
  the commit.
- Drive the real binary for each gap's user-visible change (pty script:
  limits table, ls, status, monitor brief; dashboard help, empty state,
  export dialog) and one real `caam activate` round-trip on the least
  important Codex Account.

# Round 2 — the caveats and gaps left after the review

Everything above landed on 2026-09-13/14 (see `SURFACE_AUDIT_2026-09-13.md`
for status). A gap analysis on 2026-09-14 left the items below. Same
method: red → green, one slice at a time; same closing checks.

## R1 — Mechanical leftovers (no decisions)

1. `robot.go`: the four "valid providers: codex, claude, gemini" hints use
   `supportedToolsList()`.
2. `limits --best` / `--recommend`: windows say "left, resets at" through
   `usage.WindowLeftText`, like the table.
3. `internal/exec`: the handoff's dead `handoffConfig` field goes; the
   `LoginHandler` interface keeps `Provider` and gains `ResumeArgs` (R3),
   losing `TriggerLogin`, `IsLoginComplete`, `IsLoginFailed`,
   `LoginCommand`, `IsLoginInProgress`, `ExpectedPatterns` and their tests.
4. `next`: the single-profile path loads config and the database before
   switching, so it logs like the multi-profile path.
5. `SPMConfig.GetRefreshThreshold` (no caller) goes.
6. `ACCOUNT_SWITCHER.md` §5 drops the history of `b` and `l`.

## R2 — Kimi refreshes its own token (`internal/refresh/kimi.go`) (done)

Kimi's access token lives about an hour; the CLI renews it with the
refresh token at `POST {oauthHost}/api/oauth/token` (form-encoded:
`client_id=17e5f671-d194-4dfb-9706-5516cb48c098`, `grant_type=refresh_token`,
`refresh_token=…`; `oauthHost` defaults to `https://auth.kimi.com`, overridden
by `KIMI_CODE_OAUTH_HOST`/`KIMI_OAUTH_HOST`; the CLI also sends its
`X-Msh-*` device headers, which `internal/usage/kimi.go` already builds).
401, 403 or `invalid_grant` means the session is gone. The response carries
`access_token`, optionally `refresh_token`, and `expires_in`.

1. `TestRefreshKimiToken_PostsTheCLIsForm` (httptest) — the form, the
   headers, the parsed response; 401 → `ErrRefreshTokenReused`-class error.
2. `TestRefreshProfile_KimiUpdatesTheVaultCopy` — `kimi-code.json` in the
   vault gets the new `access_token`, `refresh_token` and `expires_at`
   (seconds), nothing else touched; the live file too when the Account is
   Active (the existing `RefreshProfile` rule).
3. `caam refresh kimi <account>` works; the dashboard's `refreshableProvider`
   includes kimi, so `r` refreshes instead of offering a login; the refresh
   still happens only behind `NeedsRefresh` (expired or refused).

Status: landed 2026-09-14 (`internal/refresh/kimi.go`, `caam refresh kimi`,
`--all` includes kimi, the dashboard's `r` refreshes a refused Kimi token).
Kimi's health entry is no longer marked self-refreshing, so an expired
vault copy warns and points at the refresh, as Codex's does.

## R3 — Switch, then resume: the handoff and the pane tools (done)

A running session holds its credential in memory, so switching the file
under it is not enough, and injecting `/login` is a new OAuth login (a
logout first, and the thing the switcher replaces). The right move is:
switch through the core, then restart the session on its own history.
Resume flags, verified on this machine: Claude Code `--continue`, Codex
`resume --last`, Gemini `--resume latest`, Kimi `--continue`.

1. `TestHandoff_SwitchesThenResumes` (`internal/exec`, mock CLI) — on a
   rate limit the runner switches through the core, ends the child, and
   respawns it with the provider's resume args; the notification says
   "Switched to X and resumed". `LoginHandler.ResumeArgs()` supplies the
   flags; `SmartRunner.Run` gains a restart loop around spawn-and-wait.
2. `TestCoordinator_SwitchesAndResumesARateLimitedPane` — `Config.Recover`
   (wired from cmd: pick the next Account by rotation, `switcher.Switch`,
   return the resume command) replaces the `/login` injection; the
   coordinator sends `/exit`, waits for the prompt, sends the resume
   command. When `Recover` reports no other Account
   (`ErrNoOtherAccount`), the old capture-first `/login` path remains.
3. `caam wezterm switch-all <tool>` — one switch per tool through the
   core, then `/exit` + resume in each rate-limited pane; `login-all`
   stays for the no-other-account case and says so.

Status: all three slices landed (`d612c90` handoff; coordinator `Recover`
and `wezterm switch-all` in the following commit). The coordinator's
resume pause is `Config.ResumeDelay` (1.5 s); `wezterm switch-all` uses
the same pause. Kimi has a resume command but no rate-limit pattern in
the coordinator yet, so only the handoff and switch-all cover it.

## R4 — One word for the thing with accounts: "agent"

`CONTEXT.md` says Agent and lists "provider" under words to avoid. Every
string a person reads in the dashboard and in CLI help says agent
("Agents (5)", "←/→ agent", "Log in to which agent?", `caam add <agent>`
in help). Flag and subcommand names (`--tool`, `provider`) stay: they are
the CLI's contract. One test per surface pins the wording.

## R5 — The handoff document (done)

`caam-handoff.md` is rewritten around the product mission per ADR-0001: what
the switcher is, where it lives (`docs/ACCOUNT_SWITCHER.md`), the rules,
what is done, what needs Chris. The v1 file stays as history.

Status: rewritten 2026-09-14; the v2 text it replaced is kept as
`caam-handoff-v2-superseded.md`.

## R6 — Waiting on Chris

- Five Codex accounts need one `n` login each; then the Codex round trip
  (`caam activate` each way, `limits codex` healthy for both, vault copies
  rotated).
- Two decisions: Claude's label ("Claude Code") and the `limits` table's
  dropped SCORE/BURN/DEPLETES columns.
- `golangci-lint migrate` on `.golangci.yml`: run on a scratch copy
  2026-09-14 with golangci-lint 2.9.0. The migration adds `version: "2"`,
  drops `run.timeout`, and adds the default exclusion presets. Against the
  branch it reports 12 findings, all staticcheck: 11 quick-fix style
  suggestions (tagged switches, De Morgan, `fmt.Fprintf`) in upstream code,
  and one real one, a nil dereference in the dashboard's `applyState`,
  fixed on the branch with a test. Decision for Chris: commit the migrated
  config as is (and either silence QF* or fix the 11 upstream spots).
- Remove the two merged agent worktrees under `.claude/worktrees`.
