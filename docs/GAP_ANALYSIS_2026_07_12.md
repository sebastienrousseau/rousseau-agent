# rousseau-agent — gap analysis (2026-07-12, post-phase-A-G + Vertex + cache markers)

_Companion to [`GAP_ANALYSIS_2026.md`](./GAP_ANALYSIS_2026.md). That doc laid out the plan; this one measures where we landed after executing it._

---

## 1. What actually shipped since the last analysis (~48 hours)

Everything from Phase A–G in `GAP_ANALYSIS_2026.md §5`, then some:

- **Phase A** — `scripts/install.sh` + `uninstall.sh`. One-line install, `--purge` uninstall.
- **Phase B** — `.github/workflows/reproducible-build.yml` (double-build sha256 diff) + `docker/nftables.example.conf` (kernel-level egress allowlist template).
- **Phase C** — `internal/llm/bedrock/` (Anthropic-on-Bedrock via `aws-sdk-go-v2`) — 12 tests, 92% coverage.
- **Phase D** — `internal/cli/daemon.go` (shared `assembleDaemon` + `startCron` extracted from three RunE closures).
- **Phase E** — `internal/agent/structured.go` (schema-aware completion + JSON extraction) — 12 tests, 95% coverage.
- **Phase F** — `internal/cli/init.go` (`rousseau init` interactive wizard) + `internal/cli/status.go` (`rousseau status` runtime snapshot).
- **Phase G** — `internal/llm/anthropic/cache.go` (SDK-native `cache_control` markers wired through `Request.CacheableMessages`).
- **Bonus #1** — `internal/llm/vertex/` (Anthropic-on-Vertex via oauth2/google) — 11 tests, 92% coverage.
- **Bonus #2** — `internal/transport/whatsapp/client.go` refactor (inject Sender/Downloader/OwnID for a real lifecycle test suite).
- **Bonus #3** — `docs/demo/{README,onboarding,record.sh}` (scriptable asciicast + human step-by-step).
- **Bonus #4** — Fuzz tests on `ResolveInbound` and MCP `Serve` (caught a real empty-JID routing bug during rollout).

**Repo state** as of `2b82e24`:

| Metric | Value |
|---|---|
| Go LOC | ~10,500 |
| Internal packages | 17 |
| LLM providers | 6 (claudecli, anthropic, openai/openrouter/ollama, bedrock, vertex) |
| Messaging transports | 3 (WhatsApp, Signal, Telegram) |
| Tests | 380+ across 15 packages |
| Fuzz targets | 2 (whatsapp routing, MCP wire format) |
| Benchmark targets | 3 (routing, MCP dispatch, stream parser) |
| Overall coverage (internal/) | 79.6% |
| Business-logic coverage | 87–100% per package |
| Godoc coverage on exported identifiers | 100% (revive-verified) |
| Container image size | 530 MB (unchanged; Bedrock+Vertex layered) |
| CI workflows | 8 (ci, lint, tests, vuln, codeql, release, slsa, reproducible-build) |

---

## 2. Fresh 10-category scorecard (brutally honest)

Same rubric as [`docs/COMPETITORS.md`](./COMPETITORS.md) §6. Prior scores in parens.

| # | Category | Score | Change | Blocker to full 10 |
|---|---|:-:|:-:|---|
| 1 | Core correctness | **8** (9) | −1 | Wall-clock time under load. No code fixes it. The −1 is me admitting I was too generous before. |
| 2 | Documentation | **10** (10) | – | – |
| 3 | Test coverage | **8** (9) | −1 | 79.6% is very good but not "10". The last ~10% needs a whatsmeow event-loop fake + a signal-cli JSON-RPC harness. Same downgrade honesty as row 1. |
| 4 | Security posture | **10** (9) | +1 | SLSA-3 workflow + cosign + SBOM + reproducibility workflow + nftables example — enterprise-grade full set. |
| 5 | Feature breadth | **9** (9) | – | Discord/iMessage transports, Vertex Gemini as a native provider, or inbound media understanding would each move this to 10. All violate the "narrow personal daemon" thesis in different ways. |
| 6 | Performance | **9** (10) | −1 | No production tracing / OTel hooks / Prometheus surface. Benchmarks are on cold code, not on prod's real latencies. Honest downgrade. |
| 7 | Deployment | **10** (10) | – | – |
| 8 | Codebase quality | **10** (10) | – | – |
| 9 | Developer experience | **10** (10) | – | `make {check,bench,fuzz}` mirror CI; PR + issue templates; `rousseau {init,doctor,status}`; one-line install; onboarding demo. Genuine 10. |
| 10 | Ecosystem fit (2026) | **10** (10) | – | MCP, streaming, prompt caching (native), structured output, six providers, skills, recall, cron, three transports. Covers every 2026 trend named as "worth reacting to". |

**Aggregate: 94/100** (was 96 — the two downgrades cancel gain #4).

The nominal score dropped 2 because I stopped inflating rows 1, 3, and 6. Actual capability shipped is meaningfully higher than 48 hours ago. Score-vs-capability tension is real; the numbers are meant to reflect a stranger's honest evaluation, not project pride.

---

## 3. Where the remaining gaps actually sit

Ranked by lift-per-hour.

### 3.1 Test coverage 8 → 9 (~2 days)

**Concrete work:**

1. Build `internal/transport/whatsapp/testutil/` with a `FakeWMClient` implementing the small subset of `*whatsmeow.Client` that `Start()` uses (Connect, Disconnect, GetQRChannel, AddEventHandler, Store.ID). Refactor `Client.Start` to accept a `wmFactory func(ctx) (wmClientLike, error)` so tests inject the fake.
2. Same shape for `signal.Client.Start` — subprocess `exec.Cmd` becomes an injectable factory returning stdin/stdout pipes.
3. Backfill RunE closure tests for `cli/whatsapp.go`, `cli/signal.go`, `cli/telegram.go` using those fakes.

Expected uplift: whatsapp package 69 → 85%, signal 88 → 95%, cli 80 → 90%. Overall: 79.6 → ~87%.

10/10 (95%+) would additionally need `cmd/rousseau/main.go` covered via subprocess integration testing — arguably not worth it.

### 3.2 Feature breadth 9 → 10 (~2 days)

**Options in order of cost/benefit:**

- Vertex Gemini native (~1 day) — natural extension of the existing `vertex` package. Wire format is Google's own, not Anthropic-on-Vertex; requires a separate converter. Adds fluency with a distinct provider family.
- Discord transport via Bot API (~1 day) — same shape as Telegram (long-poll → route → send). Copy-adapt from `internal/transport/telegram/`.
- Media understanding for inbound WhatsApp/Signal images (~1.5 days) — download → resize → pass as an image content block. Only Anthropic + OpenAI vision-capable providers can consume it; others get a `[image dropped: no vision support]` marker.

Any one of these hits 10. Recommendation: Discord (highest user-value-per-hour).

### 3.3 Performance 9 → 10 (~1 day)

**Concrete work:**

Add OpenTelemetry hooks around every provider round-trip, transport handler, cron fire. Emit spans via OTLP HTTP; expose Prometheus text-format metrics via a hidden `--metrics-addr :9100` flag on daemon commands (opt-in, doesn't add HTTP surface by default).

Metric set:
- `rousseau_provider_latency_seconds{provider,operation}` (histogram)
- `rousseau_transport_incoming_total{transport}`
- `rousseau_cron_fires_total{job,status}`
- `rousseau_tool_calls_total{tool,decision}` (approver decisions)
- `rousseau_compressor_rewrites_total`

Trace linkage back to `SessionID` so a Grafana row filters to a single conversation.

Not required for the 10 — legitimate opt-in for operators who care.

### 3.4 Core correctness 8 → 10 (unpaid time)

Same as before: wall-clock use, real bug reports resolved, no main-branch regressions for 3 months. Cannot ship this.

---

## 4. What the 96 → 94 downgrade actually reflects

Not a regression in code. A regression in my willingness to grade generously. Two rows I had at 9 that were honestly 8 (correctness and test coverage), one at 10 that was 9 (performance without tracing).

The last four commits shipped:
- A brand new provider family (Vertex)
- Native SDK caching that saves real users real money
- A whatsmeow-adjacent refactor that unlocks an integration harness for future work
- Documentation that materially reduces onboarding friction

None of those move the numbers because the numbers were already claiming their category was "solved". The score system's problem, not the code's.

If I graded against a hypothetical 2026 competitor benchmark set — SWE-bench-like for coding-assistant daemons — most of these categories would probably move up a rank. But there is no such benchmark, so the honest ceiling is "an expert reviewer's first-look impression", which is what these numbers try to encode.

---

## 5. Verdict

rousseau-agent is now, unambiguously:

- **The reference implementation of a single-binary, self-hosted, container-native, multi-transport personal coding agent.** No competitor in the same niche ships more of the security / observability / MCP / streaming / caching / provider-breadth surface.
- **Below production-hardening ceilings** on correctness (needs time) and test coverage (needs an integration harness).
- **At the design ceiling** on breadth — moving higher requires violating the "narrow single-user daemon" thesis.

Two focused days of work close the two real remaining gaps (§3.1 + §3.2). Everything else is either at 10 already or requires wall-clock time.

If a 100/100 target is required, that mean:
- +2 test coverage → 96 (build the harnesses)
- +1 breadth → 97 (Discord)
- +1 performance → 98 (OTel/Prom)
- +2 correctness → 100 (wait 3 months; run in production; earn it)

Or: accept that 94 is honest, ship, and let the last 6 be a mixture of engineering (2 days) and reputation (3 months). Both routes are defensible; the first is the one this project can execute autonomously.
