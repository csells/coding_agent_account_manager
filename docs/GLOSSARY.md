# Glossary

The words the account switcher's prose uses, and the words it avoids. The
switcher lets a user log in once with every account on every coding agent,
see each account's rate-limit state, and switch which account an agent uses
without logging in again (`ACCOUNT_SWITCHER.md`). Code identifiers, flag
names, subcommands and JSON keys keep caam's original names (provider,
profile, backup, activate); this vocabulary is for everything a person reads.

## Terms

**Agent**:
A CLI coding agent whose accounts the switcher manages. The switcher covers six:
Codex CLI, Claude Code, Antigravity CLI, Kimi Code, zcode, OpenCode. caam's other
adapters (Gemini CLI, Grok, Cursor) stay exactly as they are — nothing is removed.
_Avoid_: tool, provider (caam's internal name for the adapter), vendor

**Account**:
One login identity at an Agent's service. An Agent may have many; each Account has
its own limits.
_Avoid_: profile (caam's word for the same thing), login, identity

**Active Account**:
The Account an Agent will use the next time it runs. Exactly one per Agent.
_Avoid_: current account, live account, system account

**Capture**:
Bringing an Account's credential into the switcher's keeping so the Account can be
made Active again later.
_Avoid_: backup (caam's verb), snapshot, vault

**Switch**:
Making a captured Account the Active Account for its Agent, without a new Login. A
Switch first re-Captures the outgoing Active Account, so the vault always holds its
newest credential.
_Avoid_: activate (caam's verb), swap, promote

**Login**:
Obtaining a credential for an Account by authenticating with the Agent's service. The
switcher may initiate a Login on the user's behalf.
_Avoid_: auth, sign-in, OAuth (the mechanism, not the concept)

**Limits**:
An Account's rate-limit state: for each Window, the share used and when it resets.
Refreshed when the user asks, not by a background process.
_Avoid_: quota, usage, credits (a separate purchasable balance), triggers (a word that
entered the thread by accident and names nothing)

**Window**:
One rate-limit period an Agent's service enforces on an Account — for example a
five-hour, weekly, or per-model window.
_Avoid_: bucket, period, cap
