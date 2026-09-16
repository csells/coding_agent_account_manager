---
status: accepted
date: 2026-09-13
---

# Antigravity Limits need the Code Assist project, asked for as Antigravity

`retrieveUserQuota` with an empty body answered `403 PERMISSION_DENIED` "You do not
have a valid license of this product (#3501)" for an Account in good standing, so the
dashboard showed no Antigravity Limits at all. The Antigravity CLI itself is a closed
Go binary talking gRPC (the public `google-antigravity/antigravity-cli` repository holds
only documentation), so the request shape came from three public clients that fetch
the same quota (CodexBar, and two OpenCode plugins), then was confirmed live.

Two facts, both now in `internal/usage/agy.go`:

1. The quota call must name the Account's Code Assist project. `loadCodeAssist`, sent
   with the Antigravity client metadata (`ideType: ANTIGRAVITY`,
   `platform: PLATFORM_UNSPECIFIED`, `pluginType: GEMINI`), returns it as
   `cloudaicompanionProject`; `retrieveUserQuota` is then called with
   `{"project": …}`. The project is cached per access token.
2. Google keys the `loadCodeAssist` answer on the User-Agent. Under caam's own
   User-Agent the same token gets allowed tiers only — no project, no current tier —
   as if the Account were never onboarded. Under `antigravity` it gets the project and
   the tier. caam sends `antigravity` for these two calls; it is not decoration.

Google's opaque `chat_NNNNN` buckets are not models and are dropped. The token lives
an hour and agy renews it when it runs; caam does not refresh it, and on macOS reads
the login-keychain item through its mirror.

## Considered options

- **Refresh the Antigravity token ourselves** to get past `UNAUTHENTICATED`.
  Rejected: that means holding agy's OAuth client and storing a credential agy did
  not write; running agy once renews it.
- **Onboard the Account (`onboardUser`) when `loadCodeAssist` returns no project.**
  Rejected for now: a state-changing call on the user's Google account, and every
  Account that has run agy is already onboarded. The dashboard says "no Code Assist
  project" and points at running agy.
