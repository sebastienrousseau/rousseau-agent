<!-- markdownlint-disable MD033 MD041 -->
<!-- SPDX-License-Identifier: Apache-2.0 OR MIT -->

<h1 align="center">rousseau-agent</h1>

<p align="center">
  <em>A self-hosted personal AI agent daemon that bridges nine chat
  transports to the LLM provider of your choice, in a single static Go
  binary.</em>
</p>

<p align="center">
  <a href="https://github.com/sebastienrousseau/rousseau-agent/actions/workflows/ci.yml"><img src="https://img.shields.io/github/actions/workflow/status/sebastienrousseau/rousseau-agent/ci.yml?branch=main&style=for-the-badge&logo=github&label=CI" alt="CI" /></a>
  <a href="https://github.com/sebastienrousseau/rousseau-agent/actions/workflows/slsa.yml"><img src="https://img.shields.io/badge/SLSA-Level%203-blueviolet?style=for-the-badge" alt="SLSA Level 3" /></a>
  <a href="#development"><img src="https://img.shields.io/badge/coverage-98.1%25-66c2a5?style=for-the-badge&labelColor=555555" alt="Coverage 98.1%" /></a>
  <a href="https://pkg.go.dev/github.com/sebastienrousseau/rousseau-agent"><img src="https://img.shields.io/badge/pkg.go.dev-reference-informational?style=for-the-badge&logo=go" alt="Go reference" /></a>
  <img src="https://img.shields.io/badge/Go-1.26-00ADD8?style=for-the-badge&logo=go&logoColor=white" alt="Go 1.26" />
  <a href="#license"><img src="https://img.shields.io/badge/license-Apache--2.0%20OR%20MIT-blue?style=for-the-badge" alt="Apache-2.0 OR MIT" /></a>
</p>

---

## Contents

**Getting started**

- [Install](#install) — binary, container, source
- [Quick Start](#quick-start) — write a config, run a transport
- [Capabilities](#capabilities) — what is actually shipped, by layer

**Reference**

- [Transports](#transports) — nine chat surfaces behind one interface
- [LLM providers](#llm-providers) — six backends, one `Provider` contract
- [Tools and integrations](#tools-and-integrations) — six built-ins, 26 native integration tools, Composio
- [MCP](#mcp) — server and client, spec revision 2024-11-05
- [Skills](#skills) — Markdown skills, optional SSH signature enforcement
- [Recall, scheduling, and sub-agents](#recall-scheduling-and-sub-agents) — memory, cron, fan-out
- [Agent-to-Agent (A2A)](#agent-to-agent-a2a) — peer protocol surface
- [Configuration](#configuration) — precedence, top-level keys, worked example
- [Library usage](#library-usage) — embedding the loop via `pkg/`
- [Examples](#examples) — runnable example index

**Operational**

- [Deployment](#deployment) — rootless Podman, systemd Quadlet, Kubernetes
- [Benchmarks](#benchmarks) — the harness, and what it does not yet claim
- [Observability](#observability) — metrics, traces, redaction
- [When not to use rousseau-agent](#when-not-to-use-rousseau-agent) — limitations
- [Development](#development) — make targets, CI matrix, fuzzing, soak
- [Security](#security) — trust model, hardening, supply chain
- [Documentation](#documentation) — all reference docs
- [Acknowledgements](#acknowledgements)
- [License](#license)

---

## Install

`rousseau-agent` builds to a single static binary. `CGO_ENABLED=0` is the
default and SQLite is provided by `modernc.org/sqlite`, so there is no C
toolchain at build time and no libc coupling at runtime.

| Channel | Install |
|---|---|
| Go toolchain | `go install github.com/sebastienrousseau/rousseau-agent/cmd/rousseau@latest` |
| Source | `git clone https://github.com/sebastienrousseau/rousseau-agent && cd rousseau-agent && make build` |
| Container (full) | `podman pull ghcr.io/sebastienrousseau/rousseau-agent:latest` |
| Container (distroless) | `podman pull ghcr.io/sebastienrousseau/rousseau-agent:distroless-latest` |
| Signed release archive | GitHub Releases — archive, `checksums.txt`, CycloneDX SBOM, cosign signature, SLSA provenance |

Go **1.26** is the floor; `go.mod` declares `go 1.26` and pins
`toolchain go1.26.6`. CI compiles the binary for twelve GOOS/GOARCH/tag
combinations on every push, so a Raspberry Pi Zero (`linux/armv6`) or a
SiFive board (`linux/riscv64`) is never a release cycle away from an
unbuildable tree.

### From source

```bash
git clone https://github.com/sebastienrousseau/rousseau-agent
cd rousseau-agent
make build             # produces ./bin/rousseau
./bin/rousseau version
```

`make check` runs the same gate CI does: `go vet`, `golangci-lint`,
`go test -race`, `govulncheck`.

### Container images

Five images are defined. Three are runtime flavours, two are the build
environment CI and agents use to produce them.

| Image | Dockerfile | Base | Contains |
|---|---|---|---|
| `rousseau-agent:latest` | `docker/Dockerfile` | `node:22-alpine` | Daemon **plus** the `claude` CLI — the "unbox and run" flavour |
| `rousseau-agent:distroless` | `docker/Dockerfile.distroless` | `distroless/static-debian12:nonroot` | Daemon only; TLS roots from the base |
| `rousseau-agent:lite` | `docker/Dockerfile.lite` | `distroless/static-debian12:nonroot` | Daemon with WhatsApp compiled out (`-tags no_whatsmeow`) |
| `agent-base` | `docker/Dockerfile.base` | Ubuntu | Shared foundation for the build images |
| `agent-builder` | `docker/Dockerfile.builder` | `agent-base` | Polyglot toolchain, sandbox prerequisites, supply-chain tooling |

```bash
make images              # all five, podman by default
make image-distroless    # just the distroless runtime
make images ENGINE=docker
```

`.github/workflows/container-release.yml` publishes the `full` and
`distroless` flavours to `ghcr.io` for `linux/amd64` and `linux/arm64` on
every tag. `:lite` is a local build target. Every published image is
cosign-signed keylessly under GitHub OIDC and carries a SLSA build
provenance attestation:

```bash
cosign verify \
  --certificate-identity-regexp 'https://github\.com/sebastienrousseau/rousseau-agent/\.github/workflows/.+' \
  --certificate-oidc-issuer 'https://token.actions.githubusercontent.com' \
  ghcr.io/sebastienrousseau/rousseau-agent:distroless-latest
```

Size budgets are a CI gate, not a suggestion:
`.github/workflows/image-size.yml` fails the pull request if
`:distroless` exceeds 70 MB or `:lite` exceeds 60 MB. Per-architecture
binary sizes are in [`docker/README.md`](./docker/README.md).

### From a signed release

Release artefacts are produced by GoReleaser: a checksum file, a
CycloneDX SBOM per artefact generated by `syft`, and a cosign signature
over the checksums.

```bash
cosign verify-blob \
  --certificate-identity-regexp 'sebastienrousseau/rousseau-agent' \
  --certificate-oidc-issuer 'https://token.actions.githubusercontent.com' \
  --signature checksums.txt.sig \
  checksums.txt
```

---

## Quick Start

Three commands take a fresh checkout to a daemon answering messages.

```bash
rousseau init      # interactive setup; writes ~/.config/rousseau/config.yaml
rousseau doctor    # report every runtime dependency and config choice
rousseau chat      # Bubble Tea TUI against the configured provider
```

`rousseau init` is the only interactive step. It picks a provider,
checks the environment, and writes the config file. `rousseau doctor`
prints what it found and what it could not find, which is the fastest
way to diagnose a missing `claude` binary or an unreadable state
directory.

Once the config is in place, run a transport instead of the TUI:

```bash
# WhatsApp — QR-scan pairing on first launch
rousseau whatsapp --allow 447000000000@s.whatsapp.net

# Slack over Socket Mode — outbound WebSocket, no public webhook
rousseau slack --app-token xapp-... --bot-token xoxb-...

# Discord Gateway v10
rousseau discord --token bot-token

# IMAP inbound, SMTP outbound
rousseau email --imap-addr imap.example.com:993 --imap-username u --imap-password p \
               --smtp-addr smtp.example.com:587 --smtp-username u --smtp-password p \
               --from bot@example.com
```

Every transport reads its defaults from the config file; flags override
them. `rousseau <transport> --help` lists the per-transport flags.

The full command tree:

| Command | Purpose |
|---|---|
| `chat` | Bubble Tea TUI against the configured provider |
| `whatsapp`, `signal`, `telegram`, `matrix`, `slack`, `discord`, `sms`, `imessage`, `email` | Run one chat transport as a daemon |
| `mcp` | MCP server over stdio, JSON-RPC 2.0 |
| `session list \| show \| search \| delete \| cost` | Inspect stored conversations and per-session spend |
| `cron add \| list \| remove` | Manage scheduled prompts |
| `skills list \| show` | Inspect loaded skills |
| `status` | Runtime snapshot — session count, cron state, database health |
| `doctor` | Diagnose the local installation |
| `init` | Interactive first-run setup |
| `version` | Version, commit, build date (stamped via `-ldflags`) |

---

## Capabilities

| Layer | What ships |
|---|---|
| **Agent loop** | Multi-turn planner with structured tool use, streaming responses, per-session context, LLM-backed session compression, plan mode with checkpoints (`internal/agent/plan`), lifecycle hooks (`internal/agent/hooks`). |
| **Sub-agent fan-out** | `subagent.Spawn(ctx, parent, provider, tasks, policy)` runs N detached-copy tasks with bounded concurrency, per-task timeout, and an aggregate token budget. Two aggregators ship (human-readable and JSON). Also exposed to the model as the `spawn_subagent` tool. |
| **Memory and recall** | FTS5 keyword search across every stored session, plus hybrid vector recall (`internal/recall`) — SQLite blob store, cosine similarity, weighted blend against the keyword score. Letta-style self-editing memory in `internal/memory/letta`. |
| **Tool registry** | Concurrency-safe registry. Six built-ins (`read`, `write`, `edit`, `grep`, `bash`, `spawn_subagent`), 26 native integration tools, tools imported from external MCP servers, and the opt-in Composio adapter. |
| **Approval policy** | `allow_all`, `deny_all`, or `pattern` mode with per-tool allow and deny regular expressions over a configurable default verdict. |
| **Bash sandbox** | Four backends selected by `tools.bash.sandbox`: `none`, `nsjail`, `gvisor`, `firecracker` (`internal/tools/sandbox`). |
| **OAuth broker and vault** | `internal/auth/oauth` — provider-agnostic broker with an XChaCha20-Poly1305 vault. Master key from `$ROUSSEAU_TOKEN_KEY`, the OS keyring, or a mode-0600 file. Key rotation preserves plaintext. |
| **Session store** | SQLite via `modernc.org/sqlite` (pure Go, embedded). WAL journaling, `busy_timeout=15s`, checkpoint on `Close`. Tables cover sessions, JID mapping, cron jobs, OAuth tokens, recall vectors, and `session_costs`. |
| **Resilience** | Per-JID token bucket (`internal/ratelimit`), panic-recovery middleware, and a circuit breaker per provider (`internal/resilience`, `sony/gobreaker/v2`). |
| **Multimodal input** | Every provider adapter maps `ContentImage` to its native wire shape. `internal/media` enforces 10 MiB per image and 40 MiB per turn, with the MIME type sniffed from the first 512 bytes. Optional voice-note transcription via `whisper-cpp` or the OpenAI audio API. |
| **Identity** | Stable identity IDs across transports (`internal/identity`) so one conversation can move from WhatsApp to Slack to email. `/whoami`, `/link`, `/unlink` chat commands resolve without an LLM round trip. |
| **Multi-tenancy** | `internal/tenant` scopes sessions, state, and policy per tenant. |
| **Cost accounting** | Every completion records provider, model, token usage (input, output, cache-read, cache-creation) and an estimated USD figure from `internal/pricing`. Query with `rousseau session cost`. |
| **Observability** | Prometheus registry with 15 `rousseau_*` metric families, an OpenTelemetry OTLP/HTTP tracer, and a redacting `slog` handler carrying default rules for every credential shape the daemon touches. |
| **TUI** | Bubble Tea client with viewport, scrollback, streaming indicator, and typing feedback. |

There is no SaaS control plane, no telemetry endpoint, no license server,
and no vendored broker. The only outbound traffic is the LLM call and the
transports the operator enabled.

---

## Transports

Nine chat surfaces sit behind one interface. Every adapter implements
`transport.Transport` (`Start`, `Stop`, `Deliver`) and every one of them
talks to the same tool registry, session store, and approval policy.

| Transport | Inbound | Outbound | Protocol / library |
|---|:---:|:---:|---|
| WhatsApp | yes | yes | `go.mau.fi/whatsmeow` |
| Signal | yes | yes | `signal-cli` JSON-RPC subprocess |
| Telegram | yes | yes | Bot API, long polling |
| Matrix | yes | yes | Client-Server API |
| Slack | yes | yes | Socket Mode — outbound WebSocket, no public webhook |
| Discord | yes | yes | Gateway v10, WebSocket plus intents |
| iMessage | yes | yes | BlueBubbles HTTP polling |
| Email | yes | yes | IMAP inbound, SMTP outbound |
| SMS | no | yes | Twilio REST or Vonage REST |

Not one of them requires an inbound HTTP listener. Transports that need
to receive messages use an outbound WebSocket or poll. That is why the
reference Quadlet unit publishes no ports and why a Kubernetes install
needs no `Service`.

The WhatsApp adapter is the only one behind a build tag. `-tags
no_whatsmeow` compiles it out, which is what the `:lite` image does. The
CLI surface survives the removal: `rousseau whatsapp` still exists, but
`Start`, `Deliver`, and `Transcribe` return an explicit "unavailable"
error rather than silently doing nothing, so an operator who enables the
transport in a `:lite` build learns why immediately.

`internal/transport/router.go` owns per-JID session keying, the
allowlist, and dispatch. Adding a tenth transport is an adapter and its
tests; nothing in the agent core moves.

---

## LLM providers

| Provider | Package | Auth | Notes |
|---|---|---|---|
| `claudecli` *(default)* | `internal/llm/claudecli` | Inherited from the `claude` CLI | No API key passes through the daemon's config. The recommended path for individual operators. |
| `anthropic` | `internal/llm/anthropic` | `ANTHROPIC_API_KEY` | Direct API with prompt-cache markers on the last two messages. |
| `openai` / `openrouter` / `ollama` | `internal/llm/openai` | Endpoint-specific | Any OpenAI-compatible endpoint. The `ollama` preset points `base_url` at `http://localhost:11434/v1`. |
| `bedrock` | `internal/llm/bedrock` | Standard AWS credential chain | Enterprise-managed Claude on AWS. |
| `vertex` | `internal/llm/vertex` | GCP service-account JSON | Enterprise-managed Claude on GCP. |
| `router` | `internal/llm/router` | Delegates to its children | Rule-based dispatch across the providers above. |

The contract is `agent.Provider` and `agent.StreamingProvider`. Adding a
backend is one `Chat` and one `ChatStream` implementation.

`provider: router` is the multi-model case: send short chit-chat to a
cheap model, tool-heavy turns to an expensive one, everything else to the
default. Rules match on `message_len_min` / `message_len_max`,
`tool_use_count_min` / `tool_use_count_max`, and `session_id_prefix`.
Each decision is labelled on the
`rousseau_router_decisions_total{rule, chosen_key, chosen_provider}`
counter, so a routing policy that never fires is visible rather than
theoretical.

---

## Tools and integrations

Built-ins live in `internal/tools/builtin`: `read`, `write`, `edit`,
`grep`, `bash`, and `spawn_subagent`.

Twenty-six native integration tools ship across five suites, each
registered only when its credentials are present:

| Suite | Tools | Count |
|---|---|---:|
| GitHub | `github_list_prs`, `github_get_pr`, `github_list_repos`, `github_get_repo`, `github_search_code`, `github_list_issues`, `github_get_issue`, `github_create_issue`, `github_comment_issue` | 9 |
| Google Workspace | `gmail_list`, `gmail_get`, `gmail_send`, `drive_search`, `drive_get`, `calendar_list_events`, `calendar_create_event` | 7 |
| Linear | `linear_list_issues`, `linear_get_issue`, `linear_create_issue`, `linear_update_issue` | 4 |
| Slack | `slack_post_message`, `slack_get_thread`, `slack_add_reaction`, `slack_list_channels` | 4 |
| Stripe | `stripe_list_charges`, `stripe_get_customer` | 2 |

A sixth adapter, Composio (`internal/tools/integrations/composio`), is
opt-in and reaches a much larger catalogue behind a single credential.
It is deliberately not counted above: the tool set is whatever the remote
account exposes, not something this repository can enumerate.

[`examples/embed-integrations`](./examples/embed-integrations) registers
every enabled suite into a `tools.Registry` from environment-driven
credentials.

---

## MCP

Both halves of the Model Context Protocol are implemented against spec
revision **2024-11-05**.

**Server** (`internal/mcp`, `rousseau mcp`) speaks JSON-RPC 2.0 over
stdio and exposes the daemon's tools and sessions to any MCP-capable
client — Claude Desktop, IDE extensions, or another agent. The protocol
decoder carries a fuzz target (`internal/mcp/fuzz_test.go`).

**Client** (`internal/mcp/client`, adapted into the registry by
`internal/tools/mcp`) consumes tools published by external MCP servers.
Servers declared under `mcp.clients` are spawned when the daemon starts;
each tool they advertise is registered as `mcp:<name>:<tool>`.

```yaml
mcp:
  clients:
    github:
      command: npx
      args: ['-y', '@modelcontextprotocol/server-github']
      env:
        GITHUB_PERSONAL_ACCESS_TOKEN: ${GITHUB_TOKEN}
    playwright:
      command: npx
      args: ['-y', '@modelcontextprotocol/server-playwright']
      start_timeout_seconds: 30
      request_timeout_seconds: 60
```

The client is fail-soft. A server that will not start logs a warning and
is skipped; the daemon keeps running with the rest. A broken MCP server
should degrade the tool catalogue, not take down the transport.

---

## Skills

Skills are Markdown files with YAML frontmatter in the
[agentskills.io](https://agentskills.io) format. They are discovered from
`agent.skills_dir`, composed into the system prompt, and version-tracked.
`rousseau skills list` and `rousseau skills show <name>` inspect what the
daemon actually loaded. Four skills are bundled — see
[`skills/README.md`](./skills/README.md).

Because a skill is prompt text that the model will follow, skill loading
can require a signature. `internal/skills/verify.go` shells out to
`ssh-keygen -Y verify` against an OpenSSH allowed-signers file, which is
the same mechanism Git uses for SSH-signed commits.

```yaml
agent:
  skills_dir: ~/.local/share/rousseau/skills
  skills_require_signature: true
  skills_allowed_signers_file: /etc/rousseau/allowed_signers.pub
  skills_signature_namespace: rousseau-skills
```

```bash
ssh-keygen -Y sign -f ~/.ssh/rousseau-skills -n rousseau-skills git-rebase.md
# produces git-rebase.md.sig
```

With `skills_require_signature: true` an unsigned or badly signed skill
is dropped rather than loaded.

---

## Recall, scheduling, and sub-agents

**Recall** (`internal/recall`) is hybrid: a vector score and a keyword
score, blended by `hybrid_weight` (default `0.7`, vector-heavy).
Embedders are `noop` — zero vectors, for exercising storage without
embedding cost — and `voyage`, which calls the Voyage `/embeddings`
endpoint. Ingestion chunks at `chunk_tokens` with `chunk_overlap`;
retrieval returns `retrieval_k` rows; `purge_after` drops rows past an
age. The whole subsystem is off unless `recall.enabled` is set.

**Cron** (`internal/cron`) is a `robfig/cron/v3` scheduler goroutine over
durable job storage. Expressions are validated against the parser before
a job is persisted, so a malformed schedule fails at `rousseau cron add`
rather than silently never firing. Scheduled prompts are delivered
through any registered transport.

**Sub-agents** (`internal/agent/subagent`) fan a parent session into N
detached-copy tasks under three simultaneous limits: a concurrency cap, a
per-task timeout, and an aggregate token budget. Results come back
through an aggregator — one human-readable, one JSON.

---

## Agent-to-Agent (A2A)

`internal/a2a` lets a daemon participate in multi-agent work as either a
peer or a caller. The server publishes a capability card and accepts
tasks over HTTP:

```text
GET  /.well-known/agent-capabilities   JSON CapabilityCard
POST /tasks                            accept task, return {task_id}
GET  /tasks/{id}                       poll last-known status
GET  /tasks/{id}/events                SSE stream of TaskUpdates
POST /tasks/{id}/cancel                cancel a running task
```

Bearer-token auth applies to every route except the capability card when
an allowlist is configured. [`examples/embed-a2a`](./examples/embed-a2a)
stands up a server, submits a task to itself, and prints the update
stream.

Read this as a working subset rather than a complete implementation: the
package documents itself as covering the fields callers have needed so
far, and the upstream spec has many more. Design notes are in
[`docs/a2a.md`](./docs/a2a.md).

---

## Configuration

Configuration resolves in the order **flag, then environment, then file,
then default**. The file lives at `~/.config/rousseau/config.yaml`;
`--config` overrides the path.

| Key | Covers |
|---|---|
| `provider` | Which backend to use: `claudecli`, `anthropic`, `openai`, `openrouter`, `ollama`, `bedrock`, `vertex`, `router` |
| `anthropic`, `openai`, `openrouter`, `ollama`, `bedrock`, `vertex`, `claudecli` | Per-provider credentials, model, endpoint |
| `router` | `default`, `rules`, and the named `providers` the rules select |
| `agent` | System prompt, `max_iterations`, skills directory and signature policy, compression, approver |
| `log` | `level` and `format` |
| `state` | Path to the SQLite database |
| `recall` | Embedder, chunking, retrieval breadth, hybrid weight, purge window |
| `mcp` | External MCP servers to spawn and absorb |
| `hooks` | Lifecycle scripts for `pre_tool_use`, `post_tool_use`, `pre_turn`, `post_turn`, `on_error` |
| `media` | Image budgets and `media.audio` transcription backend |
| `integrations` | Per-suite credentials: `github`, `slack`, `google`, `linear`, `stripe`, `composio` |
| `ratelimit`, `resilience` | Token-bucket and circuit-breaker tuning |
| `observability` | Prometheus and OTLP settings |
| `whatsapp`, `signal`, `telegram`, `matrix`, `slack`, `discord`, `sms`, `imessage`, `email` | Per-transport credentials and allowlists |

A representative file:

```yaml
# Default "claudecli" shells out to the claude CLI and inherits its auth.
provider: claudecli

anthropic:
  api_key: ${ANTHROPIC_API_KEY}
  model: claude-sonnet-4-6
  max_tokens: 4096

claudecli:
  binary: claude
  model: sonnet
  permission_mode: bypassPermissions

log:
  level: info
  format: json

state:
  path: ~/.local/share/rousseau/sessions.db

agent:
  system_prompt: ""            # empty falls back to the built-in default
  max_iterations: 32
  skills_dir: ~/.config/rousseau/skills
  compression:
    enabled: true
    trigger_messages: 60
    keep_recent: 8
  approver:
    mode: pattern
    default: deny
    allow:
      - {tool: read, match: ".*"}
      - {tool: grep, match: ".*"}
      - {tool: edit, match: "^./workspace/.*"}
    deny:
      - {tool: bash, match: "rm -rf|sudo"}

recall:
  enabled: true
  embedder: voyage
  hybrid_weight: 0.7
  retrieval_k: 6

slack: {app_token: "", bot_token: "", allowlist: []}
email:
  imap_addr: imap.example.com:993
  smtp_addr: smtp.example.com:587
  from: bot@example.com
  poll_interval: 30s
```

### Lifecycle hooks

Each event fires its scripts in order. The first
`{"decision":"deny","reason":"..."}` short-circuits and surfaces to the
model as a synthetic tool error.

```yaml
hooks:
  pre_tool_use:
    - name: no-secrets
      command: /etc/rousseau/hooks/no-secrets.sh
      timeout_seconds: 5
  post_turn:
    - name: cost-audit
      command: /etc/rousseau/hooks/log-cost.sh
```

The payload arrives on stdin as
`{"event":"pre_tool_use","session_id":"...","tool_name":"bash","input":{...}}`
and the verdict is read from stdout as
`{"decision":"allow|deny|modify","reason":"...","modified":{...}}`.

Hooks fail open. A hook script that crashes or hangs past its timeout
cannot deadlock the daemon. That is a deliberate trade: hooks are an
operator convenience layered on top of the approver, and the approver —
which fails closed — is the security control.

### Voice notes

```yaml
media:
  audio:
    backend: whisper-cpp        # whisper-cpp | openai-api | "" (disabled)
    model_file: /models/ggml-base.en.bin
    language: en                # empty auto-detects
    timeout_seconds: 60
```

---

## Library usage

The agent loop, tool registry, and provider abstractions carry no CLI
dependency. `pkg/` is the public façade over the `internal/`
implementation: external consumers import `pkg/` verbatim, while
`internal/` remains the source of truth and is explicitly not a stable
API surface.

```go
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/sebastienrousseau/rousseau-agent/pkg/agent"
	"github.com/sebastienrousseau/rousseau-agent/pkg/llm/claudecli"
	"github.com/sebastienrousseau/rousseau-agent/pkg/tools"
	"github.com/sebastienrousseau/rousseau-agent/pkg/tools/builtin"
)

func main() {
	provider := claudecli.New(claudecli.Config{
		PermissionMode: "bypassPermissions",
	})

	registry := tools.NewRegistry()
	registry.MustRegister(builtin.NewReadTool())
	registry.MustRegister(builtin.NewGrepTool(0, 0))

	ag := agent.New(provider, registry,
		slog.New(slog.NewJSONHandler(os.Stdout, nil)),
		agent.Options{SystemPrompt: "You are a careful, concise coding assistant."})

	session := agent.NewSession("hello")
	session.Append(agent.NewUserText("What does main.go do?"))

	reply, err := ag.Turn(context.Background(), session)
	if err != nil {
		fmt.Fprintf(os.Stderr, "turn: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(reply.Content[0].Text)
}
```

The layering is load-bearing. `agent` depends only on the interfaces
`tools` exposes, its own `Provider` types, and the standard library.
Concrete providers, stores, and transports depend on `agent` — never the
reverse.

Available façades: `pkg/agent`, `pkg/agent/subagent`, `pkg/llm/claudecli`,
`pkg/recall`, `pkg/state/sqlite`, `pkg/tools`, `pkg/tools/builtin`,
`pkg/tools/integrations`.

---

## Examples

Nine self-contained programs, each exercising one slice of the API. All
of them run under `go test`, so a broken example fails CI.

```bash
go run ./examples/<name>
```

| Example | What it shows |
|---|---|
| [`embed-agent`](./examples/embed-agent) | Embed the loop, pick a provider, register tools, drive one `Turn`. |
| [`embed-subagent`](./examples/embed-subagent) | Fan a parent session into N tasks with bounded concurrency, per-task timeout, and an aggregate token budget. |
| [`embed-recall`](./examples/embed-recall) | Ingest into the SQLite vector store, then run a hybrid query. Uses the noop embedder, so it runs without an API key. |
| [`embed-integrations`](./examples/embed-integrations) | Register every enabled native suite into a `tools.Registry` from environment credentials. |
| [`embed-router`](./examples/embed-router) | Three routing rules, three test requests, printing which child provider fired. |
| [`embed-hooks`](./examples/embed-hooks) | Two `pre_tool_use` hooks — one permissive, one denying `rm -rf` — demonstrating first-deny-wins. |
| [`embed-cost`](./examples/embed-cost) | Record usage into `session_costs`, then query per-session and top-cost roll-ups via `pricing.DefaultTable`. |
| [`embed-identity`](./examples/embed-identity) | Provision a WhatsApp identity, link a Slack handle, verify both resolve to one identity ID. |
| [`embed-a2a`](./examples/embed-a2a) | Stand up an A2A peer, submit a task to itself, print the update stream. |

Full index: [`examples/README.md`](./examples/README.md).

---

## Deployment

The reference production deployment is a rootless Podman container under
a systemd Quadlet unit. It survives reboots, isolates the daemon without
a Kubernetes dependency, and stays entirely under operator control.

```bash
make container-check     # verify the host can run the hardened units
make image-daemon        # build localhost/rousseau-agent:local
make quadlet-install     # copy units into ~/.config/containers/systemd, daemon-reload
systemctl --user start rousseau-agent
journalctl --user -u rousseau-agent -f
```

`make container-check` runs `docker/preflight.sh`, which checks the
things that otherwise fail at `systemctl --user start` with an opaque
message: a `/etc/subuid` and `/etc/subgid` range for the user,
unprivileged user namespaces enabled, a `pasta`/`passt` backend, a
reachable user systemd instance, the Quadlet generator (podman 4.4 or
later), `/dev/fuse`, and the seccomp profile both units reference. It
exits non-zero with the count of failed checks, so it works as a CI gate.

`make quadlet-install` refuses to run under anything but podman. Plain
image builds work under Docker (`make images ENGINE=docker`), but
Quadlet, `UserNS=keep-id`, and the `pasta` network stack are podman
features.

### Runtime posture

Every line below is in `docker/rousseau-agent.container`.

| Setting | Value | Why |
|---|---|---|
| `Network=pasta` | Rootless network stack | `slirp4netns` was removed from recent podman; pasta blocks inbound from the host by default |
| `UserNS=keep-id` | Container UID 1000 maps to host UID 1000 | Bind-mounted files keep host ownership |
| `ReadOnly=true` | Root filesystem read-only | The daemon cannot mutate its own image |
| `Tmpfs=/tmp:rw,size=64m,mode=1777` | Writable scratch | Everything durable lives on a bind mount |
| `DropCapability=all` | No capabilities | Outbound sockets need none |
| `NoNewPrivileges=true` | No privilege escalation | Closes setuid paths |
| `SeccompProfile=` | Default seccomp filter | Kernel-level syscall gating |
| `Volume=%h/.local/share/rousseau` | State persists | WhatsApp pairing and sessions survive restarts |
| `Volume=%h/.claude` | `claude` CLI auth | The CLI refreshes cached OAuth tokens |
| `Volume=%h/team-rousseau-workspace:/workspace` | Only the workspace is visible | Nothing else on the host is mounted |
| No `PublishPort=` | No inbound listener | Every transport is outbound or polling |

The estate deliberately splits the runtime daemon from the build
environment, because the two want opposite things. The daemon holds a
WhatsApp session and Claude OAuth credentials, so it runs read-only with
every capability dropped. A build environment needs a writable filesystem
and a large `/tmp`. The invariant that falls out is worth stating
plainly:

> No single container is both writable and credentialed.

`agent-builder` carries no long-lived credentials; scoped tokens are
injected per run through `EnvironmentFile=` in its Quadlet. Do not add
credential mounts to it, and do not relax `ReadOnly=` on the daemon.

For kernel-level egress restriction on top of the container namespace, a
sample nftables allowlist ships at `docker/nftables.example.conf`.

### Kubernetes and OpenShift

The daemon is a stateless single binary; a `Deployment` plus a
`PersistentVolumeClaim` for the state directory is sufficient. Because
there is no inbound HTTP surface, outbound-WebSocket transports need no
`Service` and no `Ingress`.

---

## Benchmarks

Three agentic benchmark tracks are wired, each behind `//go:build bench`
so none of them runs as part of the default `go test ./...` gate.

| Track | Corpus | Grading |
|---|---|---|
| `swe-bench` | [SWE-bench](https://github.com/princeton-nlp/SWE-bench) Verified, 500 tasks | Apply the generated patch, run the project's suite, check FAIL_TO_PASS and PASS_TO_PASS |
| `aider-polyglot` | [polyglot-benchmark](https://github.com/Aider-AI/polyglot-benchmark), 225 tasks across C++, Go, Java, JS, Python, Rust | Apply changes, run the per-task test command, check exit status |
| `terminal-bench` | [terminal-bench](https://github.com/laude-institute/terminal-bench) | Compare the final terminal state to the expected snapshot |

**This README publishes no pass rates.** The harness exists, it is
reproducible, and it emits JSON; a number would only belong here once it
comes from a recorded run against a named commit and a named model.
`.github/workflows/benchmarks.yml` runs the bundle weekly on `main` and
uploads the JSON artefacts. To reproduce locally:

```bash
export ROUSSEAU_BENCH_DIR="$HOME/bench-corpora"
git clone https://github.com/princeton-nlp/SWE-bench "$ROUSSEAU_BENCH_DIR/SWE-bench"
ROUSSEAU_BENCH_LIMIT=10 go test -tags bench -timeout 6h ./test/benchmarks/swe-bench/...
```

Each runner skips itself unless the corpus is checked out where it
expects and a provider credential is set. Full-corpus runs cost real
money — the estimate table is in
[`test/benchmarks/README.md`](./test/benchmarks/README.md) — so
`ROUSSEAU_BENCH_LIMIT` and `ROUSSEAU_BENCH_MODEL` exist to smoke-test the
harness cheaply.

Go microbenchmarks are separate and free:

```bash
make bench     # go test -run=^$ -bench=. -benchmem ./...
```

Publishing three benchmarks rather than one is a deliberate choice.
SWE-bench Verified is the industry reference but is Python-heavy;
Aider Polyglot corrects for that; Terminal-Bench probes the failure mode
of an agent whose success on synthetic issues does not transfer to
open-ended shell work.

---

## Observability

Fifteen `rousseau_*` metric families are registered on a Prometheus
registry:

| Metric | Records |
|---|---|
| `rousseau_provider_latency_seconds` | Per-provider completion latency |
| `rousseau_provider_errors_total` | Provider call failures |
| `rousseau_prompt_cache_tokens_total` | Cache-read and cache-creation tokens |
| `rousseau_circuit_state`, `rousseau_circuit_trips_total` | Circuit-breaker state and trips |
| `rousseau_ratelimit_denied_total` | Token-bucket rejections |
| `rousseau_panics_recovered_total` | Panics caught by recovery middleware |
| `rousseau_transport_incoming_total`, `rousseau_transport_outgoing_total` | Messages per transport |
| `rousseau_session_active` | Live sessions |
| `rousseau_tool_calls_total` | Tool invocations |
| `rousseau_subagent_spawned_total` | Sub-agent fan-outs |
| `rousseau_router_decisions_total` | Routing rule hits by rule and chosen provider |
| `rousseau_compressor_rewrites_total` | Session-compression passes |
| `rousseau_cron_fires_total` | Scheduled job executions |

Tracing is OpenTelemetry over OTLP/HTTP (`internal/observability/trace.go`).

Logging goes through a redacting `slog` handler
(`internal/observability/redact`) that carries default rules for every
credential shape the daemon handles. Redaction is the default path rather
than an opt-in wrapper, because a daemon that logs a Slack app token once
has leaked it permanently.

---

## When not to use rousseau-agent

Cases where something else fits better. These are listed because the
honest answer is "not yet" or "not the design", not because the
priorities are in dispute.

- **You want a hosted product.** There is no control plane, no web UI, no
  managed onboarding, and no support contract. Someone has to run the
  daemon, hold the credentials, and read `journalctl`. If that person
  does not exist, this is the wrong tool.
- **You need inbound webhooks.** Every transport here is outbound
  WebSocket or polling, which is what allows the reference deployment to
  publish no ports. A webhook-delivered integration would need an HTTP
  listener the daemon does not ship.
- **You need a complete A2A implementation.** The server and client cover
  the routes and card fields callers have needed so far. The upstream
  spec is larger. See [`docs/a2a.md`](./docs/a2a.md) before designing
  against it.
- **You need an embedding provider other than Voyage.** Recall supports
  `voyage` and `noop`. Anything else means implementing the three-method
  `recall.Embedder` interface — small, but it is your work, not a config
  switch.
- **You cannot accept a shell tool in the trust boundary.** The `bash`
  tool runs commands with the daemon's privileges. Pattern-mode approval
  with `default: deny` and one of the four sandbox backends narrows that;
  it does not eliminate it. Read [`SECURITY.md`](./SECURITY.md) and
  [`docs/security/sandbox.md`](./docs/security/sandbox.md) and decide
  deliberately.
- **You want `:lite` from a registry.** The lite flavour is a local build
  target (`make image-lite`); the release workflow publishes `full` and
  `distroless` only.

If a case belongs on this list, please open an issue. That is how it gets
fixed or moved into the supported set.

---

## Development

```bash
make help              # list every target
make build             # build ./bin/rousseau
make check             # vet + lint + race tests + govulncheck — the CI gate
make test              # go test -count=1 ./...
make test-race         # go test -race -count=1 ./...
make cover             # coverage profile and total
make cover-gate        # enforce the 95% total and per-package floors
make cover-html        # write coverage.html
make bench             # Go microbenchmarks
make fuzz              # every Fuzz function for 10s each
make images            # all five container images
make quadlet-install   # install the Quadlet units (podman only)
make container-check   # host preflight for the hardened containers
```

### Coverage

The suite covers **98.81% of 8,119 statements** with zero failures.
Reproduce it:

```bash
make cover
```

The floor is enforced, not observed: `make cover-gate` and the CI `test`
job both run `scripts/coverage-gate.sh coverage.out 95 95`, which
requires 95% in total **and** 95% per package. Exemptions live in that
script, are listed one by one, and are visible in review.

### Fuzzing

Six fuzz targets cover the parsers that touch untrusted bytes:

| Target | Package |
|---|---|
| MCP JSON-RPC frame decoding | `internal/mcp` |
| Slack event payloads | `internal/transport/slack` |
| Discord gateway payloads | `internal/transport/discord` |
| Email parsing (two targets) | `internal/transport/email` |
| WhatsApp message handling | `internal/transport/whatsapp` |

```bash
make fuzz     # discovers and runs each for 10s
```

### Soak

`test/integration/soak` runs a synthetic workload and asserts five leak
invariants: goroutine count, allocation growth, file descriptors, GC
pressure below 5%, and heap-in-use no more than double the baseline.
Duration scales with the trigger — 10 minutes on push, 30 minutes on
pull request, 24 hours nightly.

### CI

| Workflow | Trigger | Purpose |
|---|---|---|
| `ci.yml` | push, PR | `go vet`, `golangci-lint`, `go test -race` on Linux and macOS, the 95/95 coverage gate, `govulncheck`, build |
| `cross-arch.yml` | push | 12 GOOS/GOARCH/tag combinations, including `linux/riscv64` and `linux/armv6` |
| `image-size.yml` | push, PR | Fails if `:distroless` exceeds 70 MB or `:lite` exceeds 60 MB |
| `reproducible-build.yml` | push | Two independent builds must produce an identical sha256 |
| `soak.yml` | push, PR, nightly | Wall-clock leak detection |
| `container-release.yml` | tag | Build, push, cosign-sign, and attest the `full` and `distroless` images |
| `agent-images.yml` | tag | Same treatment for `agent-base` and `agent-builder` |
| `release.yml` | tag | GoReleaser — archives, checksums, CycloneDX SBOMs, cosign signature |
| `slsa.yml` | tag | SLSA Level 3 build provenance |
| `benchmarks.yml` | weekly | SWE-bench, Aider Polyglot, Terminal-Bench; uploads JSON artefacts |

Linting is `golangci-lint` v2 with 18 linters enabled and `default: none`
— every linter in the set was turned on deliberately. Two `forbidigo`
rules are worth knowing before your first pull request: `fmt.Print*` is
banned outside `main` and the TUI in favour of `slog`, and `panic` is
banned outside `main` and test helpers.

Commit style is [Conventional Commits](https://www.conventionalcommits.org/).
Every exported identifier carries a godoc comment. See
[`CONTRIBUTING.md`](./CONTRIBUTING.md).

---

## Security

The full policy, response SLAs, and trust model are in
[`SECURITY.md`](./SECURITY.md). Report vulnerabilities privately to
**sebastian.rousseau@gmail.com**; acknowledgement within 72 hours,
triage within 7 days.

### Trust boundary

The `bash` tool executes arbitrary commands with the daemon's
privileges. That is the point of a coding agent and also its largest
exposure. Three controls narrow it:

1. **Approval policy.** `pattern` mode with `default: deny` and explicit
   per-tool allow rules. This fails closed.
2. **Sandbox backend.** `tools.bash.sandbox` selects `none`, `nsjail`,
   `gvisor`, or `firecracker`. See
   [`docs/security/sandbox.md`](./docs/security/sandbox.md).
3. **Container isolation.** The reference deployment mounts only the
   workspace, the state directory, and `~/.claude`. Nothing else on the
   host is visible from inside.

Operators running unattended chat-transport daemons must either enforce
`pattern` mode with a deny default or accept `bypassPermissions` with an
explicit understanding of the exposure.

### Credential handling

- OAuth tokens are sealed with XChaCha20-Poly1305 in
  `internal/auth/oauth`. The master key comes from `$ROUSSEAU_TOKEN_KEY`,
  the OS keyring, or a mode-0600 file. Rotation preserves plaintext.
- The redacting `slog` handler is the default logging path, with rules
  for every credential shape the daemon touches.
- Skills can be required to carry a valid SSH signature before they are
  allowed to influence the system prompt.

### Runtime hardening

Read-only root filesystem, all capabilities dropped, `NoNewPrivileges`,
default seccomp profile, non-root UID 1000, `keep-id` user-namespace
mapping, and no inbound HTTP surface anywhere in the process.

### Supply chain

| Control | Implementation |
|---|---|
| Build provenance | SLSA Level 3 via `slsa-framework/slsa-github-generator`; `actions/attest-build-provenance` on container images |
| Signing | cosign keyless under GitHub OIDC — release checksums and every published image |
| SBOM | CycloneDX JSON per release artefact, generated by `syft` |
| Reproducibility | `reproducible-build.yml` requires two independent builds to hash identically |
| Vulnerability scanning | `govulncheck` on every CI run; Dependabot for `gomod` and `github-actions` |
| Static analysis | `golangci-lint` v2 with 18 linters, plus CodeQL default setup for Go |
| Dependency pinning | Exact versions in `go.mod`; `go.sum` frozen |

Reachable trust roots: GitHub Actions OIDC for SLSA, the Sigstore public
transparency log for cosign, and the Go module checksum database for
`pkg.go.dev`.

---

## Documentation

| Document | Covers |
|---|---|
| [`SECURITY.md`](./SECURITY.md) | Disclosure policy, response SLAs, trust model, in-scope and out-of-scope surfaces |
| [`CONTRIBUTING.md`](./CONTRIBUTING.md) | Commit conventions, pull-request guidelines, local test recipe |
| [`CHANGELOG.md`](./CHANGELOG.md) | Per-release notes; the version scheme increments by exactly `+0.0.1` and carries no semver meaning |
| [`docker/README.md`](./docker/README.md) | Container tags, per-architecture binary sizes, flavour decision table, Quadlet, build-image estate |
| [`examples/README.md`](./examples/README.md) | Runnable example index |
| [`skills/README.md`](./skills/README.md) | The bundled skills |
| [`test/benchmarks/README.md`](./test/benchmarks/README.md) | Benchmark harness, reproduction steps, cost calibration |
| [`docs/a2a.md`](./docs/a2a.md) | Agent-to-Agent protocol design |
| [`docs/security/sandbox.md`](./docs/security/sandbox.md) | The four bash-sandbox backends |
| [`docs/compatibility.md`](./docs/compatibility.md) | Compatibility contract |
| [`docs/multi-tenant.md`](./docs/multi-tenant.md) | Multi-tenant mode |
| [`docs/memory-letta.md`](./docs/memory-letta.md) | Letta-style self-editing memory |
| [`docs/plan-mode.md`](./docs/plan-mode.md) | Plan mode with checkpoints |
| [`docs/progress-updates.md`](./docs/progress-updates.md) | Live progress and mid-flight interaction |
| [`docs/ROADMAP.md`](./docs/ROADMAP.md) | Implementation plan |
| [`docs/COMPETITORS.md`](./docs/COMPETITORS.md), [`docs/COMPETITORS_2026_07_12.md`](./docs/COMPETITORS_2026_07_12.md) | Landscape audit |
| [`docs/WHY_NOT_TRUSTCLAW.md`](./docs/WHY_NOT_TRUSTCLAW.md), [`docs/WHY_NOT_OPENCLAW.md`](./docs/WHY_NOT_OPENCLAW.md), [`docs/WHY_NOT_ZEROCLAW.md`](./docs/WHY_NOT_ZEROCLAW.md) | Head-to-head comparisons |
| [`docs/demo/`](./docs/demo/README.md) | Onboarding walkthrough and recording script |

---

## Acknowledgements

A daemon that speaks nine chat protocols is mostly other people's
protocol work. The load-bearing dependencies, and what they carry:

- [`go.mau.fi/whatsmeow`](https://github.com/tulir/whatsmeow) — the
  WhatsApp transport, including pairing and the Signal-protocol session
  handling that makes it possible at all.
- [`modernc.org/sqlite`](https://gitlab.com/cznic/sqlite) — a pure-Go
  SQLite. It is the single reason this project ships a static binary with
  no libc coupling and still gets WAL and FTS5.
- [`charmbracelet/bubbletea`](https://github.com/charmbracelet/bubbletea)
  — the TUI.
- [`spf13/cobra`](https://github.com/spf13/cobra) and
  [`spf13/viper`](https://github.com/spf13/viper) — the command tree and
  the flag-over-environment-over-file precedence.
- [`robfig/cron`](https://github.com/robfig/cron) — the scheduler, and
  the expression parser that lets `rousseau cron add` reject a bad
  schedule at write time.
- [`sony/gobreaker`](https://github.com/sony/gobreaker) — the per-provider
  circuit breakers.
- [`prometheus/client_golang`](https://github.com/prometheus/client_golang)
  and the [OpenTelemetry-Go](https://github.com/open-telemetry/opentelemetry-go)
  SDK — metrics and traces.

The [Model Context Protocol](https://modelcontextprotocol.io) and
[A2A](https://google.github.io/A2A/) specifications, and the
[agentskills.io](https://agentskills.io) skill format, are implemented
here against their public definitions.

---

## License

Dual-licensed under [Apache License 2.0](./LICENSE-APACHE) or
[MIT](./LICENSE-MIT), at your option.

`SPDX-License-Identifier: Apache-2.0 OR MIT`

See [`CHANGELOG.md`](./CHANGELOG.md) for release history.

<p align="right"><a href="#contents">Back to Top</a></p>
