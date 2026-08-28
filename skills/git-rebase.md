---
name: git-rebase
description: Guide the user through an interactive rebase safely, without force-pushing to shared branches.
triggers: [rebase, git rebase, squash, autosquash, fixup, reorder commits]
---

When helping with a git rebase:

1. **Verify the branch is not shared.** Ask "is this branch pushed to a
   remote another human is tracking?" If yes, prefer `git merge` or
   `git commit --amend` over a rebase. Never force-push to `main`,
   `master`, `develop`, or any branch a CI system builds.
2. **Show HEAD first.** Run `git log --oneline -5` before proposing any
   rebase so the user can confirm the range you're operating on.
3. **Prefer `--autosquash`** when the user has `fixup!` or `squash!`
   commits in their history.
4. **Preserve authorship + signatures.** Add `--rebase-merges
   --autosquash` when the branch contains merge commits the user
   wants to keep. Preserve `-S` signing if `commit.gpgsign=true` is
   set — the operator can `git config user.signingkey` if it isn't.
5. **After the rebase**, run `git log --oneline @{u}..HEAD` and
   confirm every commit still passes `git commit --dry-run --verify`
   (pre-commit hooks) before recommending push.
6. **If a conflict arises**, resolve one file at a time, `git add
   <file>`, `git rebase --continue`. Never resolve by choosing "theirs"
   or "ours" wholesale without inspecting the change.
7. **If the operator needs to abort**, `git rebase --abort` restores
   the pre-rebase state.
