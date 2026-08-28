# Compatibility contract

`rousseau-agent` uses a monotonic `+0.0.1`-only versioning scheme
(see [`CHANGELOG.md`](../CHANGELOG.md) for the full policy). The
version number therefore does not signal breaking-change intent — this
document does.

## Public surface: `pkg/`

Anything under [`pkg/`](../pkg/) is intended for external consumers
who embed `rousseau-agent` as a library rather than run it as a daemon.
It is a thin façade over selected `internal/` types (see
[`pkg/agent/agent.go`](../pkg/agent/agent.go) for how aliasing works).

Compatibility promise:

- **Exported identifiers under `pkg/` will not be renamed or removed
  without a `BREAKING:` note in the release CHANGELOG entry** that
  introduces the change.
- Deprecated identifiers get a `// Deprecated:` comment naming the
  replacement and stay in place for **at least three subsequent
  releases** before removal.
- Struct-field additions are considered non-breaking; readers must
  not depend on struct sizes.
- New methods on exported interfaces **are** breaking (implementers
  must add them). Adding them requires a `BREAKING:` note.
- Behavioural changes to existing methods (return-value shape, error
  wrapping, side-effects) are breaking. Same treatment.

Currently-public packages in `pkg/`:

- `pkg/agent` — agent loop, Provider/Handler/Tool interfaces, Session
  and Message types
- `pkg/agent/subagent` — sub-agent Spawn primitive
- `pkg/llm/claudecli` — Claude Code CLI provider (exported for
  consumers who want to plug in their own SessionCache without
  importing `internal/`)
- `pkg/state/sqlite` — SQLite session/OAuth-vault store
- `pkg/tools` — tool registry and common tool contracts

Test coverage on these packages is the highest-priority tech-debt
item (see ROADMAP T2).

## Non-public surface: `internal/`

Everything under [`internal/`](../internal/) is free to change without
a CHANGELOG note. Consumers who import `internal/*` — Go's module
system technically permits this via `replace` directives — do so
at their own risk and get no compatibility guarantee.

If a subsystem in `internal/` proves useful enough that external
consumers want to depend on it, the promotion path is:

1. Add a thin façade under `pkg/` that re-exports the intended surface
2. Cover the façade with tests
3. Document the new package in this file
4. Announce in the next release's CHANGELOG entry

## CLI surface

The `rousseau` binary's command names, flag names, and default
behaviour are treated as public API. Same rules as `pkg/`: no
rename/remove without a `BREAKING:` note; deprecation window ≥ 3
releases.

New commands, new flags, and new default-off features are
non-breaking.

Prompt output format (log lines, JSON schemas emitted by `--json`
modes) is considered public API for machine consumers. Field
additions are non-breaking; field removal or type changes are.

## Config file surface

The `config.yaml` schema (`internal/config`) is public API in the same
sense as CLI flags. Renames trigger `BREAKING:` notes; adding new
optional keys is non-breaking.

Environment-variable names (`ROUSSEAU_*` prefix, viper-derived
snake-case path) inherit the same contract.

## Container image tags

- `ghcr.io/sebastienrousseau/rousseau-agent:vX.Y.Z` — immutable per
  tag
- `ghcr.io/sebastienrousseau/rousseau-agent:latest` — the most recent
  tag; **not recommended for production** (pins should be explicit)
- `ghcr.io/sebastienrousseau/rousseau-agent:distroless-vX.Y.Z` — the
  minimal-runtime flavour of the same release

Image digests are pinned in the release notes so consumers can
`podman pull ghcr.io/…@sha256:…` and get bit-identical bytes.

## MCP protocol

- MCP server API version is negotiated per-connection; the server
  supports the version advertised by MCP `latest` at the time of the
  release. Older client versions are supported for **at least six
  months** after they are deprecated by the MCP spec.
- Once the MCP client (ROADMAP W1.3) lands, the same contract will
  apply to client-side protocol handling.

## Providers and transports

Adding a new provider or transport is always non-breaking. Removing
one is breaking. Changing the config schema of an existing provider
or transport is breaking.

## Metrics and traces

- Prometheus metric names and label sets are considered public API.
  Renames trigger `BREAKING:` notes.
- New metrics are non-breaking; new labels on existing metrics are
  breaking (they change the identity of the series).
- OpenTelemetry span names and attribute keys inherit the same
  contract.

## Filesystem paths and on-disk formats

- SQLite schema migrations are additive-first; destructive migrations
  (column drops, type narrowing) trigger a `BREAKING:` note.
- The WhatsApp session store (`~/.local/share/rousseau/whatsapp.db`)
  format is owned by upstream `whatsmeow`; we do not commit to
  compatibility across `whatsmeow` major bumps beyond what the
  library itself promises.
- The claudecli session-cache table (`claude_sessions`) is owned by
  us; schema changes are noted.

## Explicitly not covered

- LLM provider vendor APIs change under us. If Anthropic renames a
  field, our exposed types may change with them; we treat that as
  best-effort forwarding, not a stability commitment.
- Docker Hub / other-registry mirrors of the ghcr.io image are not
  officially supported; only `ghcr.io/sebastienrousseau/…` gets the
  compatibility contract.
