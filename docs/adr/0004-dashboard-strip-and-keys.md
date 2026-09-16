---
status: accepted
date: 2026-09-13
---

# The dashboard shows only Agents with Accounts, and has one refresh key

Chris's decisions on 2026-09-13, after using the dashboard against his real Accounts.

**Agents without a captured Account are not on the strip.** There is nothing to
switch between on them, and nine tabs of "no accounts yet" hid the five that mattered.
`n` is how an Agent gets its first Account: it asks which Agent to log in to — every
Agent the switcher manages, the selected one preselected, install status shown — then
runs the Login (per ADR-0002). An empty vault shows how to get started instead of
empty tabs.

**`b` and `l` are gone; `r` is the one refresh.** Upstream's `b` opened a dialog to
file the live credential under a typed name; after `n` existed it duplicated work that
Enter and `n` already do on the way through, and a typed name could file one Account's
tokens under another's. Upstream's `l` refreshed a token under the label
"login/refresh", which reads as a Login. Chris asked for one `r` that "refreshes all
the things": it re-fetches the Limits on screen and, only when the selected Account's
token has expired or the provider just refused it, refreshes the token first (Codex and
Gemini, the only Agents whose tokens the switcher can refresh) and fetches the Limits
again. A refresh spends the refresh token, so it waits for a reason.

**LAST USED comes from the activity log.** The column read upstream's isolated-profile
store, which nothing writes, so every Account said "never". It now shows the log's last
activate, deactivate, switch or login of the Account, and a Login from the dashboard is
logged as one.

## Considered options

- **Keep `b` as "capture what is signed in" made smarter** (capture under the
  recognised name, ask only when unknown). Considered and would have stayed closer to
  upstream; Chris chose to drop the key rather than keep a third way to capture.
- **Two refresh keys, `r` for the token and `R` for the Limits.** Implemented briefly;
  Chris asked why there were two, and there was no good answer.
- **Show empty Agents folded on one line.** An earlier design (commit a61a5dc) that
  the tab strip replaced; hiding them entirely is simpler and `n` covers the need.
