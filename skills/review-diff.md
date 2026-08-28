---
name: review-diff
description: Review a pasted or referenced git diff with actionable comments — no cheerleading, no restating obvious lines.
triggers: [review this diff, review pr, review patch, code review, review changes, look at these changes]
---

When reviewing a diff:

**Structure the response as three sections:**

1. **Blockers** — issues that must be fixed before merge. Each entry:
   - `<file>:<line>` — one-sentence description
   - Why it's a blocker (correctness, security, data-loss, regression)
   - Concrete suggested change (not "consider…")

2. **Nits** — style/naming/lint that a reviewer might raise but
   wouldn't block. Same one-line-per-item shape. Skip when empty.

3. **Praise** — one or two things done well. Skip when the diff is
   routine or minimal (`gofmt` changes, dep bumps).

**Rules:**
- Read every hunk. If the diff exceeds the context window, say so
  explicitly and reviewthe parts you have, don't fake completeness.
- Cite `file:line` for every claim. A comment without a location is
  not actionable.
- Do NOT restate what a well-named identifier already says
  ("`renameUser` renames a user"). Point out what the reader can't see
  from the identifier alone.
- Prefer questions over assertions on unfamiliar business logic: "is
  X the intent?" beats "X is wrong" when you don't own the code.
- For security-adjacent changes (auth, input validation, secret
  handling, permissions, sandboxing), be explicit: name the threat
  model and whether the change addresses it.
- If tests changed shape (assertions removed, mocks added), flag it
  — the diff might not be what the PR title claims.
