# Agent Skills

**Status: spec-compliant surface shipped in Phase 2.1 (Wave-2).**
The three-tier progressive-disclosure model formalised at
[agentskills.io](https://agentskills.io) is now the default.
The pre-Phase-2.1 flat-file surface (`Load` / `Select` / `Compose`
in `internal/skills/skills.go`) is retained for backwards
compatibility while callers migrate, and will be removed in a
subsequent wave.

## What Skills are for

Skills package a chunk of "when this kind of task comes up, do it
this way" instructions that the agent can lazily load only when
they apply. A skills-heavy deployment (20+ skills) that spliced
every skill's full body into the system prompt would burn ~150k
tokens per turn on instructions the model doesn't need for the
current task. Progressive disclosure fixes that: at tier 1 the
model sees only a catalog of `name` + `description` (roughly
50-100 tokens per skill); at tier 2 it loads a single skill's body
when it decides the task matches; at tier 3 it reads individual
resource files (scripts, references, assets) on demand as the
body instructs.

Full technical spec — the fields, the discovery rules, the safety
model — lives at [agentskills.io/specification](https://agentskills.io/specification).
Everything below is rousseau-specific: how the spec is implemented
in this codebase, and how to write / test / deploy skills against
it.

## Directory layout

A skill is a **directory** containing a `SKILL.md` file with YAML
frontmatter. Additional resources live next to `SKILL.md`,
conventionally under `scripts/`, `references/`, `assets/`:

```
my-skill/
├── SKILL.md              # required
├── scripts/              # optional, model reads via Bash tool
│   └── extract.py
├── references/           # optional, model reads via Read tool
│   └── api-errors.md
└── assets/               # optional, templates / schemas / images
    └── report.md.tmpl
```

The **directory name MUST equal the `name` frontmatter field**
(NFKC-normalised) per spec. rousseau's discovery walker enforces
this and silently skips mismatched skills; use `rousseau skills
validate` (forthcoming) to surface the reason.

## Frontmatter — the six-field allowlist

```yaml
---
name: my-skill                  # required, 1-64 chars, lowercase alnum + `-`
description: One line. When to  # required, 1-1024 chars. State BOTH what
  use it AND what it does.      # the skill does AND when to trigger.
license: Apache-2.0             # optional. SPDX id or free-form pointer.
compatibility: Requires gh, jq. # optional, ≤500 chars. Env requirements.
allowed-tools: Bash(gh:*) Read  # optional, experimental. Advisory only —
                                # rousseau's permission system remains
                                # the source of truth.
metadata:                       # optional, client-defined extension slot.
  author: platform-team         # Keys and values stringified. Namespace
  version: "0.0.1"              # non-standard keys with `x-<vendor>-`.
  x-rousseau-signature-id: 7f3a
---
```

Any field outside this allowlist makes the skill invalid — the
parser rejects `triggers:`, `keywords:`, `author:` (at top level),
or any misspelled variant with a legible error. This is
deliberately strict so a typo doesn't silently disable a skill.

## Discovery scopes

rousseau scans these directories in order, with project-scope
taking precedence over user-scope (universal convention). The list
matches the spec's recommended scopes plus a Claude-compatible
alias:

| Scope | Path | Purpose |
|---|---|---|
| Project (native) | `<cwd>/.rousseau/skills/` | project-specific overrides |
| Project (portable) | `<cwd>/.agents/skills/` | cross-client project skills |
| User (native) | `~/.rousseau/skills/` | user-wide rousseau skills |
| User (portable) | `~/.agents/skills/` | cross-client user skills |

The walker caps depth at 6 levels and total directories at 2000
per scope. `.git/`, `node_modules/`, `.venv/`, `vendor/`, and
`__pycache__/` are skipped by default (configurable via
`DiscoverOptions.Skip`).

Symlinked directories are silently skipped so a loop can never
wedge discovery. Symlinks to individual files inside a skill dir
ARE followed (that's how a repo can share `references/common.md`
between skills without duplication) but tier-3 resource resolution
verifies the resolved path is still inside the skill's base
directory — a symlink to `/etc/passwd` is caught by
[`ResolveResource`](../internal/skills/resource.go).

## Activation flow

rousseau injects the tier-1 catalog into the system prompt on
every turn:

```
You have access to the skills listed below. Each skill's <description>
states what it does and when to use it. When a user task matches a
skill's description, use your Read tool on the skill's <location> path
(which points to a SKILL.md file) to load the full instructions. Only
activate skills that clearly apply — do not invent skill names.

<available_skills>
  <skill>
    <name>incident-response</name>
    <description>Run the on-call triage workflow. Use when the user
    mentions an outage, SEV-1/2/3, incident, alert storm, or "something
    is on fire".</description>
    <location>/home/rousseau/.rousseau/skills/incident-response</location>
  </skill>
  ...
</available_skills>
```

The model decides which skill (if any) applies to the current
turn, then loads it in one of two ways:

1. **File-read activation** (simpler): the model uses its normal
   `Read` tool on the `<location>` path. Works today.
2. **Dedicated `activate_skill(name)` tool** (cleaner, planned):
   registered by the harness with the enum of valid skill names,
   returning the SKILL.md body wrapped in
   `<skill_content name="...">…</skill_content>`. Ships when the
   compaction layer is taught to preserve these blocks.

Once activated, the model's normal tool use handles tier 3:
`Read scripts/x.sh` / `Bash python scripts/extract.py` etc. — the
harness resolves relative paths via `ResolveResource`, which
refuses anything outside the skill's base dir.

## Provider surface

Two providers ship, side-by-side, until legacy callers migrate:

- **`skills.SpecProvider`** — spec-compliant, three-tier. Recommended.
- **`skills.Provider`** — legacy flat-file `Load` / `Select` / `Compose`
  model. Retained for callers not yet migrated. **Deprecated;**
  planned removal in a subsequent wave.

Both satisfy `agent.SkillsProvider` (`SystemAppendix(*Session) string`)
so the wiring layer can swap implementations behind a config toggle
without changes to `agent.Agent`.

## Operator config — switching modes

The daemon assembly picks the provider at boot from a single
config key, `agent.skills_mode`:

```yaml
agent:
  skills_dir: ~/.rousseau/skills   # or wherever your skills live
  skills_mode: spec                # "" | "legacy" | "spec"
                                   # default: legacy (unchanged
                                   # from pre-Phase-2.1)
```

Values:

- **`""` or `"legacy"`** — the default. Scans `skills_dir`
  non-recursively for `*.md` files; each file's `triggers: [...]`
  frontmatter drives keyword activation. This is the pre-Phase-2.1
  behaviour; existing installations keep working without any
  config change.
- **`"spec"`** — the three-tier progressive-disclosure loader.
  Scans `skills_dir` for per-skill subdirectories with `SKILL.md`;
  emits only the tier-1 catalog into the system prompt; leaves
  bodies for the model to read on demand.

The legacy signed-bundle path (`agent.skill_bundles.dir`) is a
legacy-mode concept only. When `skills_mode: spec` is set with a
non-empty `skill_bundles.dir`, the daemon logs a single WARN
(`skills.spec_ignores_bundles`) and continues without loading
the bundles. Spec-mode signed-skill support ships via
`metadata.x-rousseau-signature` in a subsequent wave.

**Migration path.** Set `skills_mode: spec` in a non-production
environment first. Convert each flat `<name>.md` into a directory
containing `SKILL.md` (see below). Once all skills are converted,
flip production. The two modes cannot mix within a single daemon.

## Constructors (Go API)

- **`skills.NewSpecProviderFromDir(dir)`** — the one-scope common
  case. Used by the daemon assembly when `skills_mode: spec`.
- **`skills.NewSpecProvider(discovered)`** — for callers that
  gather skills from multiple scopes (`.rousseau/skills/`,
  `.agents/skills/`, `~/.agents/skills/`) and pass the combined
  slice.
- **`skills.DiscoverSpec(root, DiscoverOptions{})`** — the
  underlying walker; use directly when you want the
  `OnInvalid` callback (surfaces per-skill parse / validation
  errors that the default silent-skip suppresses).

## Migration from the legacy flat-file model

The old model expected files at `~/.local/share/rousseau/skills/<name>.md`
with a `triggers: [...]` frontmatter field for keyword activation:

```
# ~/.local/share/rousseau/skills/git-rebase.md   (LEGACY)
---
name: git-rebase
description: Guide through interactive rebase.
triggers: [rebase, git rebase, squash]
---
Body ...
```

The spec-compliant version is a **directory** containing `SKILL.md`
with `triggers` removed (the model does its own matching from the
description):

```
# ~/.rousseau/skills/git-rebase/SKILL.md         (SPEC)
---
name: git-rebase
description: Guide through interactive rebase safely. Use when the
  user asks to rebase, squash, autosquash, or reorder commits.
---
Body ...
```

Migration is a one-time restructure: for each legacy file, create
a directory named after the skill, move the file inside as
`SKILL.md`, and rewrite the `description` to include *when to use
the skill* (so the model can match without `triggers:`).

## Security posture

The spec-compliant loader applies these defenses beyond what the
spec strictly requires:

1. **Six-field frontmatter allowlist** — any extra key errors out.
   Prevents a hostile skill from smuggling instructions in an
   ignored field the harness will later start reading.
2. **NFKC name-vs-directory check** — prevents a skill named
   `admin` in a `admin` directory being spoofed by one in `аdmin`
   (Cyrillic `а`).
3. **Symlink-loop-safe discovery** — walker refuses to descend
   into symlinked directories.
4. **Tier-3 path safety** — `ResolveResource` refuses absolute
   paths, `..` traversal, and symlinks that resolve outside the
   skill's base directory. This is the "malicious skill ships a
   symlink to /etc/passwd" defense.
5. **Enterprise Edition signature verification** — when a valid
   Enterprise license is present, skills lacking a valid
   `metadata.x-rousseau-signature` from a trusted publisher key
   are silently omitted from the catalog. Community Edition
   ignores the field. Design is compatible with the same Ed25519
   trust infrastructure that gates the license itself.

## Related

- [`../internal/skills/spec.go`](../internal/skills/spec.go) — parser + validator
- [`../internal/skills/discover.go`](../internal/skills/discover.go) — walker
- [`../internal/skills/catalog.go`](../internal/skills/catalog.go) — tier-1 emit
- [`../internal/skills/resource.go`](../internal/skills/resource.go) — tier-3 path safety
- [`../internal/skills/spec_provider.go`](../internal/skills/spec_provider.go) — `agent.SkillsProvider` implementation
- [Agent Skills open standard (agentskills.io)](https://agentskills.io)
- [Reference `SKILL.md` corpus (anthropics/skills)](https://github.com/anthropics/skills)
