# Bundled skills

Five starter skills shipped in the `rousseau-agent` container image at
`/etc/rousseau/skills/`. Every file is a Markdown document with an
optional YAML front matter that declares:

- `name` — the skill's stable identifier
- `description` — one-line summary shown in `rousseau skills list`
- `triggers` — case-insensitive substrings that activate the skill
  when they appear in a user message

The loader ([`internal/skills`](../internal/skills)) resolves skills
in this order at daemon start:

1. `--skills-dir` command-line flag, when set
2. `$ROUSSEAU_SKILLS_DIR`, when set
3. `~/.local/share/rousseau/skills/` — user overrides
4. `/etc/rousseau/skills/` — this bundle (baked into the image)

User overrides take precedence: dropping a `git-rebase.md` under
`~/.local/share/rousseau/skills/` shadows the bundled one.

## Bundled skills

| Skill | Purpose |
|---|---|
| [`git-rebase`](./git-rebase.md) | Safe interactive-rebase guidance; refuses to force-push to shared branches. |
| [`review-diff`](./review-diff.md) | Structured code-review reply (Blockers / Nits / Praise), cites `file:line`. |
| [`whatsapp-transcript-summary`](./whatsapp-transcript-summary.md) | Five-line brief of a forwarded chat thread with a suggested reply draft. |
| [`podman-quadlet`](./podman-quadlet.md) | Rootless Podman Quadlet hardening template that matches rousseau's own. |

## Signing (optional)

When operators run with signed-skills verification enabled (see
`skills.require_signature` + `skills.allowed_signers_file` in
[`docs/compatibility.md`](../docs/compatibility.md)), every file in
this directory ships a `.sig` companion produced by:

```bash
ssh-keygen -Y sign -f ~/.ssh/rousseau-skills -n rousseau-skills <file>
```

Verification uses `ssh-keygen -Y verify` against the operator's
allowed-signers file — same mechanism git uses for SSH-signed commits.

## Adding your own

Drop a Markdown file into `~/.local/share/rousseau/skills/`. Restart
the daemon (`systemctl --user restart rousseau-agent`). Verify with:

```bash
rousseau skills list
```

Skill files without triggers are loaded but never auto-activate — the
model can only see them if the operator's system prompt references
them explicitly.
