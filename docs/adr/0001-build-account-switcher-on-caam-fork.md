---
status: accepted
date: 2026-09-13
---

# Build the account switcher on a caam fork, staying in caam's spirit

Chris wants one tool for Codex CLI, Claude Code and Antigravity CLI that logs in once
per Account, shows every Account's Limits, marks each Agent's Active Account, and
Switches without re-login. After evaluating codexbar (read-only by design, swap is
app-only and Codex-only), cswap (Claude-only), codex-multi-auth (Codex-only, runtime
injection rather than a swap), caut (reads but cannot capture), and manifold (a
request proxy, not a switcher), caam was the only base whose shape — vault →
backup/activate/limits — already matched and whose targets were exactly these three
Agents. We forked it to `csells/coding_agent_account_manager` so Chris controls the
pace and the six-Agent scope. Every change stays within caam's own mission and command
surface — caam already promises "log in once… switch instantly… no browser, no OAuth
dance" and already ships a cross-provider `limits` table and a cross-tool `status` view.
The work is making those promises hold on a keychain-only Mac and for three more
Agents, so every commit is upstreamable; whether to actually open PRs is Chris's call.

## Considered options

- **Patch caam only** (three gaps on this Mac, upstreamable commits, stop when
  backup/activate/limits work). Rejected: it is how the first handoff lost the
  cross-agent view and the triggers Chris asked for.
- **Phased** (gaps as upstreamable commits, product features layered on top).
  Rejected: keeping upstream mergeability constrains the UX we actually want, and
  the goal is the product, not the contribution.
- **Product on the fork, no upstream constraint** (reshape UX freely, no PRs). Chosen on
  2026-09-13, then **revised the same day**: on inspection nothing in scope departs from
  caam's spirit or UX, so the "reshape freely / no upstream" framing was an over-reading
  of the option as offered. Superseded by the paragraph above.

## Superseded rationale, kept for the record

- The 2026-09-13 handoff (`../history/caam-handoff-v1-superseded.md`) framed the mission as "make backup,
  activate and limits work for all three tools on this machine" with commits kept
  upstreamable. That framing is withdrawn; the handoff must be rewritten around the
  product mission.
- Earlier in the same thread Chris said no tool should log him into another account.
  That was a device to keep a different agent focused, not a product boundary. The
  switcher may initiate an Agent's Login on the user's behalf.

## Consequences

- Nothing is removed from caam. The fork adds adapters and fixes bugs; every existing
  provider, command and flag stays. Chris's acceptance covers six Agents; the rest are
  untouched, not dropped.
- `LICENSE` (MIT with the OpenAI/Anthropic rider) stays exactly as forked. The rider
  restricts OpenAI, Anthropic and parties acting for them; whether Gas City is such a
  party is a business question, not settled here.
- "Done" is the product working across all of Chris's Accounts on every Agent, not
  a green test on one Account per Agent.
