# Implementation plan — closing the 2026-07-12 competitor gaps (2026-07-16)

Companion to [`docs/COMPETITORS_2026_07_12.md`](COMPETITORS_2026_07_12.md).
That doc identifies where OpenClaw, TrustClaw, and ZeroClaw are ahead and
prescribes the fixes at bullet-point level. This doc is the engineer-level
plan: file paths, package boundaries, function signatures, config surface,
test strategy, CI implications, effort estimate, and dependency ordering
for every gap.

Scope is the four §5 items from the competitor doc, plus the four other
matrix gaps that came out of it (image understanding, sub-agent parallelism,
vector recall, wall-clock correctness). Skill marketplace + agent-authored
skills are deferred — see §11.

Delta since the competitor doc was written:

- ✅ **§5.4a Prometheus metrics** landed in `internal/observability/metrics.go`.
- ✅ **§5.4b OpenTelemetry spans** landed in `internal/observability/trace.go`.
- ✅ **Row 1 correctness — fuzz targets** landed for slack, discord, email
  (in addition to pre-existing whatsapp + mcp).
- ✅ **Row 3 test coverage** lifted 75.9% → 79.9%; several transports
  now above 88% including matrix/telegram/imessage.

Everything else in §5 is still open. This doc plans the remaining work.

---

## Table of contents

1. [Work item 1 — Native tool integrations (Google Workspace, GitHub, Slack, Linear, Stripe)](#1-native-tool-integrations)
2. [Work item 2 — OAuth broker + encrypted token store](#2-oauth-broker--encrypted-token-store)
3. [Work item 3 — Container / binary size reduction](#3-container--binary-size-reduction)
4. [Work item 4 — Per-JID rate limiter](#4-per-jid-rate-limiter)
5. [Work item 5 — Panic recovery + circuit breaker](#5-panic-recovery--circuit-breaker)
6. [Work item 6 — Redacting slog handler](#6-redacting-slog-handler)
7. [Work item 7 — Image understanding (inbound)](#7-image-understanding-inbound)
8. [Work item 8 — Sub-agent parallelism](#8-sub-agent-parallelism)
9. [Work item 9 — Vector store + hybrid recall](#9-vector-store--hybrid-recall)
10. [Work item 10 — Wall-clock correctness harness (Row 1 → 10)](#10-wall-clock-correctness-harness)
11. [Work item 11 — Comparative docs (`WHY_NOT_*.md`)](#11-comparative-docs-why_not_md)
12. [Deferred — skill marketplace, agent self-extension](#12-deferred-work)
13. [Dependency ordering + timeline](#13-dependency-ordering--timeline)
14. [Risk register](#14-risk-register)

---

## 1. Native tool integrations

**Goal (§5.2):** ship native adapters for the tool surfaces enterprises
actually reach for. Not chasing Composio's 1000+; delivering the ~90%
subset without a runtime broker.

**Effort:** 5–7 engineer-days across the 5 suites, once §2 (OAuth broker)
lands.

### 1.1 Package layout

```
internal/tools/integrations/
├── google/
│   ├── gmail.go            gmail_list, gmail_search, gmail_get, gmail_send, gmail_reply
│   ├── calendar.go         calendar_list_events, calendar_create_event, calendar_free_busy
│   ├── drive.go            drive_search, drive_get, drive_upload
│   ├── docs.go             docs_get, docs_append, docs_replace
│   ├── auth.go             OAuth2 scopes + client factory
│   └── *_test.go
├── github/
│   ├── repos.go            github_list_repos, github_get_repo, github_search_code
│   ├── prs.go              github_list_prs, github_get_pr, github_create_pr, github_review_pr
│   ├── issues.go           github_list_issues, github_get_issue, github_create_issue, github_comment_issue
│   ├── actions.go          github_list_runs, github_rerun_workflow (opt-in)
│   ├── auth.go             GitHub App or PAT
│   └── *_test.go
├── slack/                  post_message, get_thread, add_reaction, list_channels
├── linear/                 linear_list_issues, linear_get_issue, linear_create_issue, linear_update_issue
└── stripe/                 stripe_list_charges, stripe_get_customer  (read-only)
```

Each suite exports a `Register(reg *tools.Registry, cfg Config) error`
that a single `internal/tools/integrations.RegisterAll(reg, cfg)` calls
in order. Failure to register a disabled suite is silent; failure to
register an enabled suite returns error at daemon startup so operators
see it immediately, not on first invocation.

### 1.2 Tool contract

Each tool implements `tools.Tool`:

```go
type Tool interface {
    Name() string
    Description() string
    Schema() json.RawMessage
    Call(ctx context.Context, input json.RawMessage) (string, error)
}
```

Existing built-ins (`internal/tools/builtin/`) already satisfy this;
integrations inherit the same test discipline (JSON-schema fidelity,
error surface, timeout). No changes to `internal/tools/registry.go` are
required — new tools are registered via `Registry.Register`.

### 1.3 SDK choice

| Suite | SDK | License |
|---|---|---|
| Google (all) | `google.golang.org/api/{gmail,calendar,drive,docs}/v*` | BSD-3 |
| GitHub | `github.com/google/go-github/v66` | BSD-3 |
| Slack | `github.com/slack-go/slack` | BSD-2 |
| Linear | Direct GraphQL over `net/http` — no first-party Go SDK. | — |
| Stripe | `github.com/stripe/stripe-go/v79` | Apache-2 |

All are already in the module ecosystem; none are blocked by licence
review. Stripe's SDK is 1.4 MB compiled; the others are <500 KB each.
Total added binary size: ~3–4 MB (measure after landing).

### 1.4 Config surface

Add to `internal/config/config.go`:

```go
type Config struct {
    // …existing…
    Integrations IntegrationsConfig `mapstructure:"integrations"`
}

type IntegrationsConfig struct {
    Google GoogleConfig `mapstructure:"google"`
    GitHub GitHubConfig `mapstructure:"github"`
    Slack  SlackToolsConfig `mapstructure:"slack_tools"`
    Linear LinearConfig `mapstructure:"linear"`
    Stripe StripeConfig `mapstructure:"stripe"`
}

type GoogleConfig struct {
    Enabled       bool     `mapstructure:"enabled"`
    ClientID      string   `mapstructure:"client_id"`      // env: ROUSSEAU_GOOGLE_CLIENT_ID
    ClientSecret  string   `mapstructure:"client_secret"`  // env: ROUSSEAU_GOOGLE_CLIENT_SECRET
    Scopes        []string `mapstructure:"scopes"`         // subset of gmail/calendar/drive/docs
    RedirectURL   string   `mapstructure:"redirect_url"`   // default http://127.0.0.1:8765/oauth/callback
}
```

Every credential field reads from an env var with the `ROUSSEAU_` prefix
as fallback; empty config disables the suite entirely. This mirrors the
`ROUSSEAU_WHATSAPP_ALLOW` pattern already in place after 2026-07-16.

### 1.5 CLI

Two new subcommands under existing `internal/cli/`:

```
rousseau auth google         # runs OAuth flow, stores tokens
rousseau auth github         # PAT prompt OR GitHub-App-install flow
rousseau tools list          # prints registered tool names + which are enabled
rousseau tools describe <n>  # prints the JSON schema for tool <n>
```

The `auth` command opens `http://127.0.0.1:8765/oauth/start/google`
in a browser (via `open`/`xdg-open`), spins up a temporary local
server that captures the `?code=` on the callback, and hands it to §2's
token store.

### 1.6 Tests

Per suite:

- Unit — mock the SDK client via an interface; verify request payload
  and error surface.
- Contract — replay recorded HTTP fixtures (using `httptest.Server`
  and a JSON dump of the real API response) so the tests stay hermetic.
- Fuzz — one target per suite that fuzzes the tool `Call()` input JSON
  (invariant: no panic, no unbounded output).

Coverage target: 85%+ per suite. That mirrors the existing transport
package standard.

### 1.7 Docs

New page per suite under `docs/tools/` in the docs site:

- `google-workspace.md` — scope table, first-run walk-through, sample
  prompts.
- `github.md`
- `slack.md`
- `linear.md`
- `stripe.md`

Cross-linked from the docs-site nav in `content.schema.toml`
category `tools`.

### 1.8 Sequencing within §1

Order matters for review load:

1. **GitHub first** (2 days) — no OAuth broker needed; PAT is fine
   for v1. Highest tool-count / test-count. Sets the template that
   the other four follow.
2. **Slack tools** (0.5 day) — shares OAuth with the Slack transport;
   trivial once transport auth is proven.
3. **Google Workspace** (2 days) — needs §2's OAuth broker first.
4. **Linear** (1 day) — small, but exercises GraphQL-over-http style.
5. **Stripe read-only** (0.5 day) — smallest; last.

---

## 2. OAuth broker + encrypted token store

**Goal:** provide a single OAuth2 broker shared by every integration
suite that needs it (Google, GitHub App, Linear webhook mode, Slack).
Tokens live in the existing SQLite state store, encrypted at rest.

**Effort:** 2 engineer-days. **Prerequisite for §1.3 (Google) and §1.5
(long-lived tokens).**

### 2.1 Package layout

```
internal/auth/
├── oauth/
│   ├── broker.go          Broker orchestrates provider flows
│   ├── provider.go        Provider interface (Google, GitHub, Slack, Linear)
│   ├── callback.go        HTTP server for the /oauth/callback endpoint
│   ├── crypto.go          AEAD wrap/unwrap for token blobs
│   ├── store.go           SQLite persistence (uses existing state/sqlite)
│   └── *_test.go
└── keyring/                 optional OS-keyring backend (macOS Keychain / D-Bus Secret)
    ├── darwin.go            build tag: darwin
    ├── linux.go             build tag: linux (via 99designs/keyring)
    ├── noop.go              build tag: !darwin,!linux
    └── keyring_test.go
```

### 2.2 Provider interface

```go
type Provider interface {
    Name() string                                            // "google", "github", …
    AuthCodeURL(state string) string                         // browser URL
    Exchange(ctx context.Context, code string) (*Token, error)
    Refresh(ctx context.Context, refreshToken string) (*Token, error)
    Client(ctx context.Context, tok *Token) (*http.Client, error)
}

type Token struct {
    AccessToken  string    `json:"access_token"`
    RefreshToken string    `json:"refresh_token,omitempty"`
    Expiry       time.Time `json:"expiry,omitempty"`
    Extra        map[string]any `json:"extra,omitempty"`     // provider-specific
}
```

`Client` returns an `*http.Client` whose transport auto-refreshes
against `Refresh` when the access token is <60s from expiry. The
built-in `oauth2.Config.Client()` does this out of the box; the wrapper
here adds our observability + rate-limit middleware.

### 2.3 Storage

- Row schema: `oauth_tokens (provider TEXT, account_id TEXT, ciphertext
  BLOB, updated_at TIMESTAMP, PRIMARY KEY (provider, account_id))`.
- Encryption: XChaCha20-Poly1305 via `golang.org/x/crypto/chacha20poly1305`.
  Key derivation: HKDF-SHA256 with the master key material.
- Master key resolution order:
  1. `ROUSSEAU_TOKEN_KEY` env var (raw 32-byte hex) — highest priority
     for CI / container / systemd-credential paths.
  2. OS keyring (`internal/auth/keyring`) on macOS + Linux desktop.
  3. Prompted on first run, stored under `$XDG_STATE_HOME/rousseau/key`
     with mode 0600 as a last resort.

Rotation: `rousseau auth rotate-key` re-encrypts every row with a new
key. Old key must remain readable during rotation.

### 2.4 Callback server

Bind localhost only (`127.0.0.1`), port default `8765` (overridable).
Only serves `/oauth/callback/<provider>`; unknown paths → 404. Shuts
itself down on first successful callback with a 60s hard-timeout
fallback so a stuck browser doesn't leave a listener open.

### 2.5 Threat model note

Tokens at rest are AEAD-encrypted; access requires either the env var
or the local OS keyring. If neither is available and the operator
approves the prompted-key fallback, the key file is chmod 0600 in the
XDG state dir. Root on the host can still read it — this is a solo-user
daemon, not a multi-tenant server, so that trust boundary is honest.

Callback server is bound to localhost; a hostile browser extension
could still capture the code from the redirect URL — mitigated by
using `code_challenge`+`code_verifier` (PKCE) on every provider that
supports it (all five do).

### 2.6 Tests

- Unit — each `Provider` mocks the well-known endpoints.
- Round-trip integration — Broker → callback → Exchange → Store →
  Retrieve → Refresh — using an in-process test server as the "provider".
- Encryption — verify ciphertext ≠ plaintext, verify AAD binding, verify
  rotation preserves plaintext across a re-encrypt.
- Fuzz — the callback handler on arbitrary query strings.

### 2.7 CLI

```
rousseau auth google              # first-time flow
rousseau auth list                # provider + account rows in the store
rousseau auth revoke google       # revokes at provider + deletes row
rousseau auth rotate-key          # re-encrypt every row with a new key
```

---

## 3. Container / binary size reduction

**Goal (§5.3):** 530 MB container → <150 MB. Not chasing ZeroClaw's
3.4 MB — that requires a Rust rewrite. Chasing distroless-shaped
"same features, tenth the size."

**Effort:** 2 engineer-days.

### 3.1 Current state

`docker/Dockerfile` uses `node:22-alpine` as the runtime image
(carries an entire Node runtime + npm + the `claude-code` CLI). Node
runtime alone is ~250 MB; combined with Alpine's userland the runtime
image lands at 530 MB.

### 3.2 Target images

Ship three tags per release:

| Tag | Base | Size target | Includes claude CLI? | Includes whatsmeow? |
|---|---|---|:-:|:-:|
| `:latest` (existing) | `node:22-alpine` | 530 MB | ✓ | ✓ |
| `:distroless` | `gcr.io/distroless/static-debian12:nonroot` | ~30 MB | ✗ | ✓ |
| `:lite` | `gcr.io/distroless/static-debian12:nonroot` | ~20 MB | ✗ | ✗ (build tag `!whatsmeow`) |

`:distroless` drops Node + npm; the operator installs `claude-code`
on the host or the container mounts it in via bind (documented under
`docs/deployment.md`).

`:lite` uses a Go build tag `no_whatsmeow` (already partially present)
to compile out the whatsapp transport entirely, so the resulting binary
is ~20 MB. Applies to embedded / edge deployments where WhatsApp isn't
needed.

### 3.3 Concrete changes

- `docker/Dockerfile.distroless` — new file, copies the static binary
  into `gcr.io/distroless/static-debian12:nonroot`.
- `docker/Dockerfile.lite` — same but with `-tags no_whatsmeow`.
- `.goreleaser.yaml` — add two Docker image manifests: `distroless`
  and `lite`, both signed with cosign and provenance-tracked.
- `internal/transport/whatsapp/*.go` — audit for build-tag correctness.
  Some files may need to move to `whatsapp_stub.go` behind
  `//go:build no_whatsmeow`.
- CI (`.github/workflows/ci.yml`) — build all three tags on every push
  so a size regression is caught the same day.

### 3.4 Container CI gate

Add `.github/workflows/image-size.yml` — fails a PR when:

- `:distroless` grows past 40 MB (buffer above 30 MB target).
- `:lite` grows past 25 MB.
- `:latest` grows past 600 MB (buffer above 530 MB current).

Use `docker manifest inspect` sizes; the gate script is ~30 lines of
bash.

### 3.5 Documentation

- `docs/deployment.md` — add per-tag guidance and the "when to pick
  which" table.
- `docker/README.md` — new; explains why three tags exist.

---

## 4. Per-JID rate limiter

**Goal (§5.4c):** protect the daemon from a single JID flooding the
LLM budget or the outbound transport. Token bucket, per-transport +
per-JID. Config-tunable, default rate.

**Effort:** 1 engineer-day.

### 4.1 Package layout

```
internal/ratelimit/
├── bucket.go              TokenBucket{capacity, rate, last, tokens}
├── keyed.go               KeyedLimiter — LRU of buckets, per-JID
├── middleware.go          transport.Handler wrapper
├── config.go              parse "10r/1m" style config
└── *_test.go
```

### 4.2 Config surface

```yaml
ratelimit:
  default: "10r/1m"          # 10 requests per 1 minute, per JID
  per_transport:
    whatsapp: "20r/1m"       # override
    slack:    "60r/1m"       # slack DMs vs whatsapp
    email:    "5r/5m"        # tighter for email
```

### 4.3 Behaviour

Middleware sits between `transport.Handler` router and the actual
`agent.Complete` path:

```go
func RateLimited(inner transport.Handler, limiter *KeyedLimiter) transport.Handler {
    return transport.HandlerFunc(func(ctx context.Context, msg IncomingMessage) (string, error) {
        if !limiter.Allow(msg.From) {
            observability.RateLimitDenied.WithLabelValues(msg.Transport).Inc()
            return "You're sending messages too quickly. Try again in a minute.", nil
        }
        return inner.Handle(ctx, msg)
    })
}
```

Returned message is user-facing — not an error — so the transport
delivers it as a reply. Metric `rousseau_ratelimit_denied_total{transport}`
is added to `internal/observability/metrics.go`.

### 4.4 Storage

Buckets live in memory (LRU capped at 10k entries by default). No
persistence across restarts by design — a restart clears rate-limit
history, matching the general "state you care about lives in the
SQLite session store" split.

### 4.5 Tests

- Unit — bucket math (refill, exhaustion, over-time).
- Middleware — verify denied count, verify allowed count, verify
  message returned to sender.
- Property — under N random arrivals over T seconds, count of allowed
  ≤ capacity + T * rate.

---

## 5. Panic recovery + circuit breaker

**Goal (§5.4d):** a runtime panic in one transport / provider / tool
must not take down the daemon; a failing upstream must not be hammered
for hours.

**Effort:** 1 engineer-day.

### 5.1 Panic recovery

Middleware pattern on every transport handler and every tool call:

```go
// internal/resilience/recover.go
func Recover(inner Handler, log *slog.Logger) Handler {
    return HandlerFunc(func(ctx context.Context, msg IncomingMessage) (reply string, err error) {
        defer func() {
            if r := recover(); r != nil {
                log.Error("handler.panic",
                    slog.Any("recover", r),
                    slog.String("stack", string(debug.Stack())),
                    slog.String("transport", msg.Transport))
                observability.PanicsRecovered.WithLabelValues(msg.Transport).Inc()
                err = fmt.Errorf("internal error")
            }
        }()
        return inner.Handle(ctx, msg)
    })
}
```

Wired at daemon assembly time in `internal/cli/daemon.go`. Every
`transport.Router` gets `Recover(handler, logger)` applied
unconditionally.

Metric added: `rousseau_panics_recovered_total{transport}`.

### 5.2 Circuit breaker

Use `github.com/sony/gobreaker/v2`. Wrap:

- Each `Provider.Complete` call (per-provider breaker).
- Each outbound transport `Deliver` call (per-transport breaker).
- Each `Tool.Call` for tools that make network calls (per-tool breaker).

```go
// internal/resilience/breaker.go
type Wrapped struct {
    inner Provider
    b     *gobreaker.CircuitBreaker[agent.Response]
}

func (w *Wrapped) Complete(ctx context.Context, req agent.Request) (agent.Response, error) {
    return w.b.Execute(func() (agent.Response, error) {
        return w.inner.Complete(ctx, req)
    })
}
```

Configuration knob per provider:

```yaml
resilience:
  circuit_breaker:
    max_failures: 5
    interval: 60s
    timeout: 30s
    half_open_max_calls: 1
```

Metric: `rousseau_circuit_state{provider,state}` (gauge, values
0/1/2 for Closed/Open/Half-Open).

### 5.3 Tests

- Recover — force a `panic("test")` in a fake handler; assert the
  daemon returns "internal error" not a process crash.
- Breaker — sequence 5 failures → assert `Open` state; wait interval →
  assert `HalfOpen` transitions; a success → assert `Closed`.

---

## 6. Redacting slog handler

**Goal (§5.4e):** structured logs must never leak credentials, phone
numbers, or arbitrary user PII. Regex + key-name pass on every log
record.

**Effort:** 0.5 engineer-day.

### 6.1 Package layout

```
internal/observability/redact/
├── handler.go             wraps any slog.Handler
├── rules.go               default rule list (see below)
├── rules_test.go
└── handler_test.go
```

### 6.2 Default rules

Redaction fires when:

- Key name matches `(?i)(token|secret|api[_-]?key|password|apikey|authorization|cookie|session|refresh)`.
- Value matches an obvious secret pattern:
  - Anthropic API keys: `sk-ant-[a-zA-Z0-9-]{80,}`
  - OpenAI: `sk-[a-zA-Z0-9]{40,}`
  - Slack: `xoxb-[0-9]+-[0-9]+-[A-Za-z0-9]+`, `xapp-1-`
  - GitHub PAT: `ghp_[A-Za-z0-9]{36}`, `github_pat_[A-Za-z0-9_]{80,}`
  - AWS: `AKIA[0-9A-Z]{16}`
  - JWT: `eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+`
- Value looks like an E.164 phone number (opt-in — default off, opt in
  via `logging.redact_phones: true`).

Redaction replaces the value with `«redacted:<class>»` (e.g.
`«redacted:anthropic»`, `«redacted:phone»`).

### 6.3 Wiring

`internal/cli/root.go`'s `newLogger` gets a `RedactWrap(inner)` on the
chosen handler. Default on. Env `ROUSSEAU_LOG_NO_REDACT=1` disables
(for local debugging).

### 6.4 Tests

- Every rule pattern gets a positive + negative test.
- Property — for any random slog value, the redacted output length ≤
  a bounded factor of the input (no regex-explosion DoS).
- Golden — snapshot of a synthetic 20-record log run to catch
  accidental redaction-regression.

---

## 7. Image understanding (inbound)

**Goal (matrix row):** support multimodal input — a user sending a
screenshot on WhatsApp / Signal / Slack should reach the model as an
image content block, not silently drop.

**Effort:** 2 engineer-days.

### 7.1 Message model change

`internal/agent/message.go`:

```go
const (
    ContentText       ContentKind = "text"
    ContentImage      ContentKind = "image"    // NEW
    ContentToolUse    ContentKind = "tool_use"
    ContentToolResult ContentKind = "tool_result"
)

type Content struct {
    Kind       ContentKind `json:"kind"`
    Text       string      `json:"text,omitempty"`
    Image      *Image      `json:"image,omitempty"`    // NEW
    ToolUse    *ToolUse    `json:"tool_use,omitempty"`
    ToolResult *ToolResult `json:"tool_result,omitempty"`
}

type Image struct {
    MediaType string `json:"media_type"`  // "image/png", "image/jpeg", "image/webp", "image/gif"
    Data      []byte `json:"data"`        // base64-serialised at JSON boundary
    Source    string `json:"source,omitempty"` // "whatsapp", "slack", …
}
```

### 7.2 Provider mapping

Each provider adapter must map `ContentImage` to its native shape:

- **Anthropic direct** — `type: "image", source: {type: "base64",
  media_type, data}` — the SDK already supports it via
  `anthropic.NewImageBlockBase64Param`.
- **Bedrock (Claude)** — same JSON payload, no change beyond passing
  through the buildBedrockBody path.
- **Vertex (Claude)** — same.
- **OpenAI** — `type: "image_url", image_url: {url: "data:MIME;base64,DATA"}`.
- **Claude CLI** — CLI supports `--image path` per invocation; stream
  images to a temp file, pass path, unlink after call.
- **Ollama / OpenRouter** — passthrough (both use the OpenAI schema).

Adapter changes go in `internal/llm/{anthropic,bedrock,vertex,openai,claudecli}/client.go`
in the `toXxxContent` functions.

### 7.3 Transport ingestion

- **WhatsApp** — `internal/transport/whatsapp/dispatch.go` — the
  existing `handleImageMessage` (currently stub) downloads the image
  via `wm.Download`, decodes, and emits a `ContentImage` block on the
  incoming message.
- **Slack** — the events_api envelope carries a `files` array; adapter
  downloads via authenticated `files.info` and emits `ContentImage`.
- **Discord** — attachment URLs on message_create; simple download,
  size-cap enforcement.
- **Matrix** — `m.image` events carry `mxc://` URIs; download via the
  homeserver `/media/v3/download` endpoint.
- **Telegram** — `photo` field on updates; call `getFile` to resolve
  path, download.
- **Email** — inline `image/*` MIME parts and `Content-Disposition:
  attachment` image parts; already parseable via
  `github.com/emersion/go-message`.
- **SMS / iMessage / Signal** — MMS on Twilio + BlueBubbles both carry
  image URLs; both are straightforward downloads.

### 7.4 Size / safety

- Max size default 10 MB per image, configurable.
- Total per-turn attachment size cap 40 MB.
- MIME sniff (`http.DetectContentType`) before trusting the transport's
  reported media type — attackers can lie in the envelope.
- Log an event when an image is dropped due to size.

Config:

```yaml
media:
  max_image_bytes: 10485760
  max_total_bytes: 41943040
  allowed_mime: ["image/png", "image/jpeg", "image/webp", "image/gif"]
```

### 7.5 Tests

- Golden — 1 seed image per MIME type, verify round-trip through each
  provider adapter.
- Fuzz — the transport image parsers on arbitrary bytes.
- Property — max-size / MIME-lying envelope: verify enforcement.
- Coverage — target 90% on all new files.

---

## 8. Sub-agent parallelism

**Goal (matrix row):** run N independent turns in parallel with a
shared parent context, aggregate results, deadline-guard on total wall
time. This is what Claude Code / Devin / OpenHands call "sub-agents"
and it's absent from rousseau.

**Effort:** 3 engineer-days.

### 8.1 Package layout

```
internal/agent/subagent/
├── spawn.go               Spawn(ctx, parent Session, tasks []Task) (Results, error)
├── task.go                Task{Prompt, Tools, MaxTurns, ProviderOverride}
├── result.go              Result{Turns, FinalText, ToolCalls, TokensIn, TokensOut, Err}
├── aggregator.go          Combine([]Result) — how to feed results back into parent
├── policy.go              max_concurrent, per_task_timeout, budget_tokens
└── *_test.go
```

### 8.2 API

```go
type Task struct {
    Prompt           string
    Tools            []string    // subset of registry
    MaxTurns         int
    ProviderOverride agent.Provider // nil = inherit
    Timeout          time.Duration
}

func Spawn(ctx context.Context, parent *agent.Session, tasks []Task) ([]Result, error)
```

Semantics:

- Uses `errgroup.Group` with `WithContext` for cancellation.
- Concurrency cap via `semaphore.Weighted(policy.MaxConcurrent)`.
- Each sub-agent gets a **detached copy** of the parent session (no
  shared message list — otherwise concurrent appends corrupt state).
- Timeout applied per-task; on trigger, the sub-agent's `ctx` is
  cancelled and its partial result is returned with `Err = ctx.Err()`.
- Budget check before each sub-agent starts: sum of already-consumed
  tokens + per-task max ≤ policy.BudgetTokens.

### 8.3 Aggregator

The `Combine` function turns `[]Result` into a single content block
appended to the parent session as if it were a tool result:

```json
{
  "kind": "tool_result",
  "tool_result": {
    "tool_use_id": "spawn-<uuid>",
    "output": "Task 1 → …\nTask 2 → …\nTask 3 → …"
  }
}
```

Alternatively, callers can pass a custom aggregator (e.g. pick the
best result by heuristic).

### 8.4 Tool exposure

Register `spawn` as a built-in tool so the model can invoke it
directly:

```json
{
  "name": "spawn",
  "description": "Run N independent research or code-analysis tasks in parallel …",
  "input_schema": {
    "type": "object",
    "properties": {
      "tasks": {"type": "array", "items": {"type": "object",
        "properties": {"prompt": {"type": "string"}, "tools": {"type": "array"}}}}
    }
  }
}
```

Model calls `spawn` → daemon executes → returns aggregated result to
the model on the next turn.

### 8.5 Observability

- Metric: `rousseau_subagent_spawned_total{provider}`.
- Trace: parent span for the spawn call, child spans per sub-agent,
  attributes for `task_index`, `tokens_in/out`, `err`.
- Log: `subagent.completed` with `task_index`, `duration_ms`,
  `tokens_in`, `tokens_out`.

### 8.6 Tests

- Unit — 3 fake providers that each return a canned response; assert
  order-preserving aggregation.
- Concurrency — start 100 tasks with concurrency 4; assert never more
  than 4 in-flight (via a counter + `sync.Mutex`).
- Cancellation — cancel parent ctx mid-flight; assert every child ctx
  observes cancellation and returns quickly.
- Budget — configure budget = 100 tokens, request 5 tasks each
  reporting 30 tokens; assert only 3 tasks run, remainder return
  budget-error.

---

## 9. Vector store + hybrid recall

**Goal (matrix row):** long-term semantic recall across sessions.
FTS5 alone is keyword-shaped; a vector store adds semantic-similarity
retrieval. Combined they beat either alone.

**Effort:** 4 engineer-days.

### 9.1 Storage

Use `github.com/asg017/sqlite-vec` — a sqlite extension that adds vector
similarity indices to the existing `modernc.org/sqlite` build. No new
process dependency.

Schema:

```sql
CREATE VIRTUAL TABLE recall_vectors USING vec0(
    id INTEGER PRIMARY KEY,
    embedding FLOAT[1024]  -- Voyage voyage-3-lite: 1024 dims
);

CREATE TABLE recall_index (
    id INTEGER PRIMARY KEY,
    session_id TEXT NOT NULL,
    message_id INTEGER NOT NULL,
    role TEXT NOT NULL,
    text TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    UNIQUE (session_id, message_id)
);

CREATE INDEX recall_index_session ON recall_index(session_id);
```

`recall_vectors.id` is the same as `recall_index.id` — vec0 is the
similarity index, recall_index is the metadata.

### 9.2 Embeddings

Provider interface:

```go
type Embedder interface {
    Embed(ctx context.Context, texts []string) ([][]float32, error)
    Dims() int
}
```

Implementations:

- **VoyageEmbedder** — voyage-3-lite (1024 dims, cheapest quality
  option, Anthropic-recommended).
- **OpenAIEmbedder** — text-embedding-3-small (1536 dims).
- **OllamaEmbedder** — local, uses `nomic-embed-text` (768 dims).
- **NoopEmbedder** — returns zero-vectors; used when embeddings are
  disabled.

Choice of embedder is per-provider config:

```yaml
recall:
  embedder:
    kind: voyage           # or "openai", "ollama", "noop"
    model: voyage-3-lite
    api_key: env:VOYAGE_API_KEY
```

### 9.3 Ingestion

On every `Session.AppendMessage`:

1. Split into chunks (400 tokens each, 40-token overlap).
2. Enqueue chunks in an async worker (channel + goroutine pool).
3. Worker calls `Embedder.Embed(batch)` — batch size 32.
4. Insert into `recall_vectors` + `recall_index`.

The channel is bounded (`chan []Chunk` capacity 256); when full, the
oldest un-embedded chunks are dropped and a metric increments. The
daemon never blocks a user turn on embedding.

### 9.4 Retrieval

Called by the compressor / agent before assembling a turn:

```go
func Recall(ctx context.Context, q string, k int) ([]RecalledMessage, error)
```

Implementation:

1. Embed the query with the same `Embedder`.
2. `SELECT id, distance FROM recall_vectors WHERE embedding MATCH ?
   ORDER BY distance LIMIT ?k*4`.
3. Load metadata rows.
4. **Hybrid rerank** — combine cosine distance from step 2 with the
   FTS5 rank score already available; the top `k` after weighted-sum
   rerank are returned.

Recall is inserted into the assistant context as a "background"
system message ahead of the user's actual turn.

### 9.5 Config

```yaml
recall:
  enabled: true
  embedder: …
  chunk_tokens: 400
  chunk_overlap: 40
  retrieval_k: 6
  hybrid_weight: 0.7   # 0.7 vector + 0.3 fts5
  purge_after: 180d
```

### 9.6 Migrations

Migration `internal/state/sqlite/migrations/0007_recall.sql`. Vec0
extension is loaded at DB open via `LoadExtension` — verified in
`sqlite.Open` startup.

Existing users: `rousseau state migrate` runs on daemon start; if
sqlite-vec extension is missing, recall is disabled with a warning
(non-fatal).

### 9.7 Tests

- Ingest → retrieve round-trip with a small fixture corpus (10
  messages); assert top-1 = the exact match, top-3 contains the
  paraphrase.
- Long-running — 10k messages, verify insert throughput and retrieval
  P95 latency.
- Purge — insert 100 messages spread over 200 days; assert purge
  removes only rows older than `purge_after`.

---

## 10. Wall-clock correctness harness

**Goal (Row 1 → 10):** the correctness row's blocker per the scorecard
is "wall-clock time" — i.e., the daemon must survive multi-hour real
runs without a leak, panic, or drift.

**Effort:** 3 engineer-days (largely CI/tooling; the daemon should
already pass).

### 10.1 Test targets

Add `test/integration/soak/`:

```
test/integration/soak/
├── main_test.go           TestSoak_24h — the driver
├── config.go              synthetic input generator
├── monitors.go            memory, goroutine, FD count over time
└── report.go              markdown summary at exit
```

Semantics:

- Boots the daemon against a fake httptest.Server providing every
  transport backend (matrix, telegram, whatsapp, …) via the same
  fake shape.
- Emits ~1 message per 500 ms across every configured transport for
  24 hours (real wall time; ~170k messages total).
- Every 5 minutes, records:
  - `runtime.NumGoroutine()`
  - `runtime.MemStats.Alloc / .Sys / .HeapInUse`
  - `os.Getpid()` FD count (via `/proc/self/fd`)
  - LSM tree size on disk
- At exit, asserts:
  - Goroutine count within 20% of the 30-minute-mark baseline
  - Alloc ≤ 2× baseline
  - FD count ≤ baseline + 10
- Publishes a soak report as a CI artefact.

### 10.2 Nightly CI

`.github/workflows/soak.yml`:

- Runs nightly on `main`, timeout 24h.
- Uploads the soak report + a Grafana-shaped JSON dump of the metrics.
- Regression thresholds fail the run.

### 10.3 Shorter version for PRs

A 30-minute soak on every PR — same code, `SOAK_DURATION=30m`. Cost:
~30 minutes of billable runner time per PR. Worth it.

### 10.4 Chaos monkey

Add `SOAK_CHAOS=1` mode that randomly kills provider connections,
returns errors from tools, and injects latency. Should still pass the
above invariants.

---

## 11. Comparative docs (`WHY_NOT_*.md`)

**Goal (§5.5):** publish three markdown files under `docs/` that name
the competition, engage with it honestly, and answer "why would I pick
rousseau instead."

**Effort:** 1 engineer-day, mostly writing.

### 11.1 Structure per file

```
docs/WHY_NOT_TRUSTCLAW.md
docs/WHY_NOT_OPENCLAW.md
docs/WHY_NOT_ZEROCLAW.md
```

Each ~800 lines:

- Frank one-paragraph description of the competitor (link to their
  README, no snark).
- Feature-matrix row of "when this competitor is the better call."
- Three-bullet answer to "why rousseau instead."
- A worked example — the same operator scenario, laid out on both
  systems, showing what changes.

### 11.2 Publication

Also render into the docs site as a `docs/comparisons/` section, one
page each. Linked from the docs-site nav.

---

## 12. Deferred work

Explicitly out of scope for the near-term implementation:

- **Skill marketplace** (ClawHub / agentskills.io equivalent). A
  registry + package format + trust model is a project on its own,
  not a two-week deliverable. Track separately.
- **Agent-authored skills** (self-extension). Same reason — requires
  the marketplace + sandbox story to be safe.
- **Cross-arch beyond amd64/arm64** (RISC-V is a ZeroClaw niche).
- **Vercel AI Gateway support** — anti-goal; TrustClaw's dependency
  chain is the thing rousseau exists to avoid.

---

## 13. Dependency ordering + timeline

```
       ┌──────────────────────────────────────────────────────────┐
       │  Week 1                                                  │
       │  §2  OAuth broker + token store          (2 days)        │
       │  §4  Rate limiter                        (1 day)  ──┐    │
       │  §5  Panic recover + breaker             (1 day)  ──┤    │
       │  §6  Redacting slog                      (0.5 day) ─┴──> both wire in via daemon assembly
       └──────────────────────────────────────────────────────────┘
                          │
                          ▼
       ┌──────────────────────────────────────────────────────────┐
       │  Week 2                                                  │
       │  §1  Integrations                        (5–7 days)      │
       │       ├─ GitHub tools                    (2 days, first) │
       │       ├─ Slack tools                     (0.5 day)       │
       │       ├─ Google Workspace                (2 days)        │
       │       ├─ Linear                          (1 day)         │
       │       └─ Stripe read-only                (0.5 day)       │
       └──────────────────────────────────────────────────────────┘
                          │
                          ▼
       ┌──────────────────────────────────────────────────────────┐
       │  Week 3                                                  │
       │  §3  Container/binary size               (2 days)        │
       │  §7  Image understanding                 (2 days)        │
       │  §11 Comparative docs                    (1 day)         │
       └──────────────────────────────────────────────────────────┘
                          │
                          ▼
       ┌──────────────────────────────────────────────────────────┐
       │  Week 4                                                  │
       │  §8  Sub-agent parallelism               (3 days)        │
       │  §10 Wall-clock correctness harness      (3 days)        │
       └──────────────────────────────────────────────────────────┘
                          │
                          ▼
       ┌──────────────────────────────────────────────────────────┐
       │  Week 5                                                  │
       │  §9  Vector store + hybrid recall        (4 days)        │
       │  Docs pass, benchmarks, release notes    (1 day)         │
       └──────────────────────────────────────────────────────────┘

Total engineering: ~5 weeks solo, or ~2 weeks with 2-3 engineers in
parallel (§2 and §5–6 are on the critical path; the rest parallelise
freely).
```

Per-week PR shape (recommended):

- Week 1 — ~5 PRs (broker, ratelimit, recover, breaker, redact); each
  ≤500 lines net + tests + docs.
- Week 2 — ~5 PRs (one per suite).
- Week 3 — 3 PRs (dockerfiles, image content, WHY_NOT docs).
- Week 4 — 2 PRs (subagent, soak).
- Week 5 — 2 PRs (vector store, release-prep).

Each PR keeps CI green — the reproducible-build + codeql + coverage
gates must not regress.

---

## 14. Risk register

| # | Risk | Impact | Mitigation |
|---|---|---|---|
| 1 | Google OAuth verification requires a domain-verified consent screen for external users; personal test accounts still work but scopes may prompt "unverified app" | UX friction | Ship an internal-scope path first; document verification separately. |
| 2 | sqlite-vec extension is not shipped with `modernc.org/sqlite` — needs a Go-side embedded blob | build complexity | Vendor the extension binary; add a `//go:embed` in a new package. |
| 3 | Soak test's 24-hour run cost on GH-hosted runners | CI billing | Nightly only on `main`, not per-PR. 30-min variant on PRs. |
| 4 | Circuit breaker on Bedrock/Vertex risks tripping on transient IAM/quota errors and staying open | availability | Categorise errors before feeding the breaker — 429/503 trip, 401/403 don't. |
| 5 | Composio's SaaS gravity keeps growing — rousseau's "no broker" pitch weakens if the 5 suites here miss the specific SaaS an operator needs | strategic | Ship a Composio-adapter *tool provider* in Week 5+ as opt-in; documented as such. See §5.2 in the competitor doc. |
| 6 | Image content on the OpenAI schema uses data-URL base64; ~30% size overhead vs binary → per-turn token counts spike | cost | Rescale > 1024px images before sending; add a `media.max_dimension: 1024` config knob. |
| 7 | Rate limiter drops a message rather than queueing → user perceives lost delivery | UX | Return a "you're too fast" reply on transports that support quick DMs (Slack, WhatsApp, iMessage); silently drop on email (SMTP has its own back-pressure). |
| 8 | Vector recall reintroduces cross-session data at the wrong moment → privacy surprise | product | Off by default; opt-in per session via `agent.RecallEnabled`; documented in `docs/security.md`. |

---

## Verification checklist per PR

Every PR that lands one of the items above must:

- [ ] Add or update `docs/` for any user-visible surface (config, CLI,
      metric names).
- [ ] Add unit + property/fuzz test coverage on new files (target
      ≥85%).
- [ ] Not regress overall coverage below the current 79.9% baseline.
- [ ] Not regress the reproducible-build determinism gate.
- [ ] Not add a required status check without also updating the
      ruleset (once branch protection lands).
- [ ] Include an entry in `CHANGELOG.md`.
- [ ] Include a release-note snippet in the PR body under a
      `## Release note` heading.

Once §1–§11 have shipped, the 90/100 scorecard should return to 100/100
against the current field, and stay there until a new competitor
appears — at which point another `docs/COMPETITORS_YYYY_MM_DD.md`
lands and the cycle repeats.
