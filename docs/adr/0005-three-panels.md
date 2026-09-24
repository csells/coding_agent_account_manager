---
status: accepted
date: 2026-09-24
---

# The dashboard is three panels: agents, accounts, and the selected account's detail

Chris's decisions on 2026-09-24, after using the dashboard against his real
Accounts. The selected Account's details were in two places — a tree of lines
under its row, and an `i` overlay ("full card") that repeated some of them and
added the rest — and neither was the whole. Now one panel under the list is.

**The selected Account's details are a third panel, under the list.** The
strip and the list stay as they were. Every row is one line, so ↑/↓ never
move the rows beneath the cursor, and the panel is titled with the Account
and carries everything the tree and the card had between them: the outcome
of the last action first; ACCOUNT (auth · plan · health · token, a missing
credential, a lock, notes, the vault path, the browser), LIMITS (every
Window the service reports, credits) and USAGE (last used, errors, penalty,
created); the keys last. The `i` key and the overlay are gone.

**A field with nothing to say is omitted.** No "Errors: None", no empty
Notes, no "Created" when unknown. A healthy Account's panel is five or six
lines; the panel is dense, not long. On a wide terminal the three groups sit
side by side; on a medium one ACCOUNT and USAGE stack beside LIMITS; on a
narrow one everything stacks.

**The panel never scrolls and never takes focus.** One focus model is the
point of the dashboard: arrows move, Enter switches. When the panel is
shorter than its content it drops lines by priority — created, penalty and
the browser first; then notes and the account label; the path; last used;
a lock, errors and a Window the table already shows with both its columns;
the auth line and the other Windows last; the notice, a missing credential
and the keys never — and a heading goes with its last line.

**Height: the panel yields to the list.** The panel takes the rows its
content needs, out of what the list does not need for all its rows or two
fifths of the space under the strip, whichever is more. The list never
loses its title, header and first three rows to it; below a title and two
lines the panel is not drawn at all. The list is what is scanned and acted
on, so it is never squeezed to a peephole, and a fixed split would waste
rows on an agent with two Windows and clip one with five.

## Considered options

- **A fixed split** (the panel always gets twelve rows). Wastes rows or
  clips, depending on the agent.
- **The list shrinks first, to a three-row window that scrolls, and the
  panel keeps its full content.** Rejected: the list is the thing you act on.
- **The panel scrolls, with Tab moving focus into it.** Rejected: a second
  focus model for a panel whose content fits any terminal Chris uses once
  empty fields are omitted. If everything-regardless-of-height is ever
  needed again, that is a case for bringing `i` back, not for scrolling.
- **Keep every field, always, in the card's label/value column.** Rejected
  for the "None" rows; the information is kept, the noise is not.
