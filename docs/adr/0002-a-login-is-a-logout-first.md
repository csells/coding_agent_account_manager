---
status: accepted
date: 2026-09-13
---

# A Login is a logout first: capture, clear, then log in

Adding five Codex Accounts through the dashboard's `n` key on 2026-09-13 left four
of them dead in the vault. The Codex CLI's `codex login` calls
`clear_existing_auth_before_login`, which runs `logout_with_revoke` on the session it
finds in `auth.json`; revoking the refresh token revokes the family, and the vault copy
re-captured seconds earlier is in that family. The usage API answered
`401 token_revoked`, the token endpoint `refresh_token_invalidated`, and a forced
`caam refresh` confirmed there is no request that brings such an Account back. Only a
new Login mints a new session.

Every path in the switcher that runs an Agent's native Login now does three things
in order: re-Capture the Active Account, **clear the live credential**, then run the
Login. A Login that finds no session has nothing to revoke. The clear happens only
when the re-Capture succeeded, i.e. when the switcher knows whose credential it is; a
live credential it cannot match to a captured Account is left for the Login to
replace, since clearing it would lose an Account for good. Upstream's `caam add` has
the outline (backup, clear, login) but files the outgoing Account under a timestamp
name and deletes files without clearing the keychain item, so it is not yet an
instance of this rule; the dashboard's `n` is, and the rule is written down so no
Login path skips it.

## Considered options

- **Keep the live credential and let the Login replace it.** Rejected: proven to
  revoke the previous Account on Codex, and nothing says the other Agents do not.
- **Refresh the dead vault copies instead of logging in again.** Rejected as
  impossible: a revoked refresh token cannot be refreshed by anyone; the provider's
  answer is "Your session has ended. Please log in again."
- **Clear before every Login, known Account or not.** Rejected: an unknown live
  credential would be destroyed rather than replaced, which breaks rule zero (never
  destroy the user's data) for no gain.
