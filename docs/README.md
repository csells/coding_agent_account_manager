# Documentation

Where the knowledge lives. Start with the design; the rest is either reference,
record, or upstream's original design documents.

## The account switcher (this fork's work)

- `ACCOUNT_SWITCHER.md` — the design and the facts behind it: the one rule
  everything follows from, why a login is a logout first, what each agent's
  credential is, where limits come from, the dashboard, why refreshing a token
  spends it, how to verify a change, known limitations.
- `GLOSSARY.md` — agent, account, capture, switch, login, limits, window, and
  the words to avoid.
- `adr/` — the decisions: 0001 build on a caam fork in caam's spirit; 0002 a
  login is a logout first; 0003 Antigravity limits need the Code Assist
  project; 0004 the dashboard strip and keys; 0005 the dashboard's three
  panels, the selected account's detail under the list.
- `PR_DESCRIPTION.md` — the body of the single upstream pull request, written
  to make the ideas easy to lift.
- `SURFACE_AUDIT_2026-09-13.md` and `SURFACE_PLAN.md` — the dated audit of
  every caam surface against the credential rules, and the plan that closed
  its gaps (rounds 1 and 2; R6 lists what sits outside the plan and what
  "done" means).
- `history/` — the three handoff documents that preceded this layout, kept
  verbatim; superseded by the files above.

## Working rules

`../AGENTS.md`: the credential rules in short form and the rules for working on
the `account-switcher` branch. `../CHANGELOG.md` (Unreleased) is the summary an
outside reader wants first.

## Upstream's design documents

- `SMART_PROFILE_MANAGEMENT.md` — the original smart-profile design (its
  early-refresh section is marked superseded).
- `DISTRIBUTED_AUTH_RECOVERY.md` — the coordinator and auth-agent design.
- `FEATURE_PLAN_2025Q1.md` — upstream's feature plan.
- `CLAUDE_AUTH_INVENTORY.md` / `.json` — the inventory of Claude Code's
  credential surfaces.
- `cli-flag-guidelines.md` — flag naming and behavior conventions.
