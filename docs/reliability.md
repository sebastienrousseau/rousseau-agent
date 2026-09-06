# Agent reliability metrics

**Status: Phase 2.3 foundation shipped. Live wiring pending.**

rousseau-agent implements the four-dimension agent-reliability
decomposition proposed in
[**"Towards a Science of AI Agent Reliability"** (Rabanser et al.,
arXiv:2602.16666, Feb 2026)](https://arxiv.org/abs/2602.16666)
— the framework Princeton HAL uses to grade frontier agents on GAIA
and τ-bench. The paper argues that a single "success rate" metric
obscures the reliability properties enterprise buyers actually
differentiate on: whether the agent gives the same answer twice,
whether it withstands malformed input, whether it knows when it's
uncertain, and whether it refuses actions it should refuse.

Why this matters for a regulated-vertical buyer: procurement teams
in 2026-2027 no longer accept "we score 92% on SWE-Bench" — the
benchmark culture has entered a reproducibility crisis (τ-bench
originally had 24/50 labels flawed per Cuadron et al., 2025;
PaperBench replication tops out at ~21% for the best agent). The
four-dimension decomposition, computed from live production
traffic, is what compliance officers want in an RFP.

## The four dimensions

| Dimension            | Aggregate            | Live-measurable                          |
|----------------------|----------------------|------------------------------------------|
| **Consistency (R_Con)**    | outcome + trajectory + resource | Partial (resource CV live; outcome/trajectory need semantic bucketing or repeats) |
| **Robustness (R_Rob)**     | fault + env + prompt | Partial (fault stratification live; env during rollouts; prompt is synthetic) |
| **Predictability (R_Pred)** | Brier (proper scoring rule)     | Fully live (pair confidence with outcome) |
| **Safety (R_Saf)**         | 1 − (1−S_comp)(1−S_harm) [Kaplan–Garrick] | Fully live (constraint-check hooks emit compliance + severity counters) |

**Overall = mean(R_Con, R_Rob, R_Pred).** Safety is reported
separately per the paper's guidance — a 99% safe agent that
catastrophically fails 1% of the time should never average out.

## Sub-metrics — the concrete formulas

### Consistency

- **Outcome consistency `C_out`** = 1 − mean of `σ̂²/(p̂(1−p̂)+ε)` per
  bucket, where each bucket collects ≥2 samples of the same request
  shape (hashed `(route, tool_signature, prompt_embedding)`).
  Normalized to disentangle from capability.
- **Trajectory `C_traj`** = ½ · (1 − mean JSD of action-type
  histograms) + ½ · (1 − mean normalized Levenshtein of action
  sequences).
- **Resource `C_res`** = exp(−mean CV) over resource types
  (tokens, latency, tool-call count, cost). CV = σ/μ per bucket.
  **Fully live-measurable from every request** — no repeats
  needed.

### Robustness

- **Fault `R_fault`** = min(Acc_faulted / Acc_clean, 1) over
  requests stratified by whether an upstream fault was observed
  (`Metadata["fault"] = "true"|"false"`). Live.
- **Env `R_env`** = min(Acc_currentSchema / Acc_previousSchema, 1)
  during rollout windows. Live when tool schemas change.
- **Prompt `R_prompt`** = 1 − mean Δaccuracy across paraphrase
  clusters. Synthetic; deferred to a follow-on `rousseau eval`
  command.

**Reference number:** ReliabilityBench (arXiv:2601.06112) reports
success dropping from 96.9% at ε=0 to 88.1% at ε=0.2 under
semantic perturbation — i.e. `R_prompt ≈ 0.91` on tool-using
agents. Use as a baseline.

### Predictability

- **Brier `P_brier`** = 1 − (1/N)·Σ(cᵢ − yᵢ)². The aggregate the
  paper uses for R_Pred — proper scoring rule, jointly penalises
  calibration and discrimination.
- **Calibration `P_cal`** = 1 − ECE (10-bin expected calibration
  error).
- **Discrimination `P_AUROC`** = fraction of (success, failure)
  pairs where the success has higher confidence. Ties count 0.5.

Requires pairing agent confidence `c ∈ [0,1]` with outcome
`y ∈ {0,1}`. Confidence signals a daemon can extract without an
extra LLM call:

- Log-prob of the final answer token (when the provider exposes it)
- Inverse of retry-count / reflection-count
- Explicit `<confidence>` XML tag in the system prompt (recommended
  for the wiring commit)

### Safety

- **Compliance `S_comp`** = 1 − violation_count / turn_count.
- **Harm severity `S_harm`** = 1 − mean(severity_weight | violation),
  weights: low=0.25, medium=0.5, high=1.0.
- **Aggregate `R_Saf`** = 1 − (1 − S_comp)(1 − S_harm)  —
  Kaplan–Garrick risk formulation.

Violations come from constraint-check hooks (PII detectors,
tool-authorization gates, "human confirmation required for tx > $X"
rules). Every turn either violates a rule or doesn't; every
violation carries a `severity` label. Both are fully live-measurable
if you already have these hooks — most enterprise deployments do.

## The CLI — `rousseau reliability`

```
$ rousseau reliability --synthetic
Reliability over 7d (366 samples)

  Overall    94.5%   (mean of consistency, robustness, predictability)

  consistency     98.8%   (5 samples)
      resource                        98.8%
      note: outcome consistency: no bucket has ≥2 repeat samples;
            trajectory consistency: no trajectory_* samples recorded

  robustness      91.1%   (150 samples)
      fault                           91.1%
      note: env robustness: need samples across two schema versions;
            prompt robustness: need paraphrase clusters (synthetic)

  predictability  93.5%   (10 samples)
      auroc                          100.0%
      brier                           93.5%
      calibration                     77.0%

  ──── Safety (reported separately; never folded into Overall) ────
  safety          99.8%   (201 samples)
      compliance                      99.5%
      severity                        50.0%
```

**Flags:**

- `--window 1d|7d|30d|90d` — rolling window (default 7d). Accepts
  any Go duration syntax too (`24h`, `48h`, `90m`).
- `--json` — machine-readable output for Grafana / compliance
  scrapers. NaN scores render as JSON `null` (never NaN, never 0).
- `--synthetic` — load a canned sample set exercising every
  dimension. Use to verify dashboard integration before real
  traffic is wired.

**Notes surface the reason a sub-score is n/a.** Rather than
silently zeroing missing data, the CLI prints operator-legible
explanations like "outcome consistency: no bucket has ≥2 repeat
samples" so nobody misreads "n/a" as "we tried and failed."

## What's shipped vs pending

### Shipped in this commit (Phase 2.3 foundation)

- `internal/reliability/` package with `Sample`, `Aggregator`,
  `Summary`, and every paper formula (`brier`, `ece`, `auroc`,
  `outcomeConsistency`, `resourceConsistency`, `trajectoryConsistency`,
  `faultRobustness`, `envRobustness`, `promptRobustness`,
  `safetySummary`). 94.3% test coverage on the numeric core.
- `rousseau reliability` CLI with human + JSON output modes,
  window selection, and a `--synthetic` sample set for dashboard
  verification.

### Pending in follow-on commits

1. **Synthetic runs (`rousseau eval`)** — protocol-perturbation
   harness for prompt robustness + outcome consistency (K=5 repeats
   of canned tasks). Turns the "partial" dimensions into "fully
   measured" via a nightly cron.

### Shipped since first release of this doc

1. **Live wiring** ✅ — `agent.Turn` records latency + tokens +
   tool-call count into `resource_cv_*`; `RecordingApprover`
   emits Safety `violation` samples with severity metadata on
   every denial; `agent.Turn` also emits Robustness `fault`
   samples with an upstream-fault stratification heuristic
   (timeout / EOF / refused / deadline / provider: /
   subprocess / exit status).
2. **Persistent store** ✅ — SQLite `reliability_samples` table
   with fire-and-forget inserts, `LoadSince` for CLI reads,
   `PruneBefore` for retention. Postgres port ships as a
   drop-in twin with the same interface — daemon assembly
   picks the driver automatically.
3. **Prometheus exporter** ✅ — `reliability.PrometheusRecorder`
   registers against the existing `observability.Registry` so
   the running daemon's `/metrics` endpoint scrapes the full
   reliability surface without any operator config change.
   Metric names follow the naming convention above; missing
   metadata defaults to `unknown` (never empty labels).
4. **Confidence elicitation** ✅ — opt-in via
   `agent.enable_confidence_elicitation: true` in `config.yaml`.
   Appends a terse `<confidence>0.NN</confidence>` instruction
   to the system prompt; `agent.Turn` parses the tag, clamps to
   [0,1], and emits a Predictability `pair` sample paired with
   the outcome. Missing tag is silently skipped (some turns
   legitimately don't reach the closing instruction).
5. **Retention cron** ✅ — `reliability.RunPruner` runs in the
   daemon at every 6 hours (defaults), prunes samples older
   than 30 days, silent-on-zero-rows so healthy daemons don't
   spam logs.

## Prometheus metric naming (reference)

The Phase-2.3 exporter (pending) will publish these under the
`rousseau_agent_*` namespace, aligned with the OTel `gen_ai.*`
semantic conventions where applicable. Gauges for aggregate scores,
histograms for underlying observations, counters for events:

```
rousseau_agent_reliability_score              gauge   # Overall
rousseau_agent_consistency_score              gauge
rousseau_agent_consistency_{outcome,trajectory,resource}_ratio  gauge
rousseau_agent_robustness_score               gauge
rousseau_agent_robustness_{fault,env,prompt}_ratio              gauge
rousseau_agent_predictability_brier_score     gauge
rousseau_agent_predictability_calibration_ratio  gauge
rousseau_agent_predictability_discrimination_ratio  gauge
rousseau_agent_confidence_score{outcome="success|failure"}      histogram
rousseau_agent_safety_score                   gauge
rousseau_agent_safety_compliance_ratio        gauge
rousseau_agent_safety_harm_ratio              gauge
rousseau_agent_safety_violations_total{constraint="...", severity="low|medium|high"}  counter
rousseau_agent_request_tokens                 histogram
rousseau_agent_request_duration_seconds       histogram
rousseau_agent_request_tool_calls             histogram
rousseau_agent_upstream_faults_total{kind="timeout|schema|auth|other"}  counter
```

Safety metrics are **counters** (not gauges) so alertmanager can
fire on "high-severity violations > 0 in 5m" — the failure mode
the paper explicitly warns against averaging away.

## Honest limitations

1. **Benchmark coverage is narrow.** The paper covers τ-bench and
   GAIA only. Two benchmarks is a slice, not a proof.
2. **LLM-judge dependency.** Safety severity classification and
   outcome scoring both rely on LLM-based judges, which have
   their own reliability problems (recursive: how reliable is
   the judge?). Mitigate with rule-based classifiers where
   possible; fall back to a small local model.
3. **Metric choices are subjective.** Practitioners will reasonably
   disagree; alternate decompositions exist (ReliabilityBench
   arXiv:2601.06112 uses a three-dimension subset;
   "Beyond pass@1" arXiv:2603.29231 frames it differently).
4. **Aggregate scores get gamed.** Any 4-D score tied to
   procurement will get optimised against — operators must keep
   the raw histograms as ground truth and treat aggregate scores
   as convenience summaries.
5. **This is complement, not replacement.** A green reliability
   dashboard does not certify the agent for production. Human
   oversight, sandboxed testing, incident review, and the
   compliance controls documented in
   [`compliance/`](./compliance/) all remain load-bearing.

## References

- **Rabanser, Kapoor, Kirgis, Liu, Utpala, Narayanan.** "Towards a
  Science of AI Agent Reliability." arXiv:2602.16666, Feb 2026.
  [Paper](https://arxiv.org/abs/2602.16666) ·
  [Princeton HAL dashboard](https://hal.cs.princeton.edu/reliability)
- **Gupta et al.** "ReliabilityBench." arXiv:2601.06112, Jan 2026.
  [Paper](https://arxiv.org/abs/2601.06112)
- **Rabanser et al.** "Beyond pass@1: A Reliability Science
  Framework for Long-Horizon LLM Agents." arXiv:2603.29231.
  [Paper](https://arxiv.org/html/2603.29231)
- **Where Does Agent Reliability Come From?** (Leni enterprise
  case study). arXiv:2607.17044, Jul 2026.
  [Paper](https://arxiv.org/html/2607.17044)
- **OpenTelemetry GenAI semantic conventions** — `gen_ai.*` metric
  namespace this package's Prometheus exporter aligns with.
  [Spec](https://github.com/open-telemetry/semantic-conventions-genai/blob/main/docs/gen-ai/gen-ai-metrics.md)

## Related rousseau-agent docs

- [`BUYER.md`](./BUYER.md) — regulated-vertical persona that
  reliability metrics primarily serve.
- [`COMMERCIAL.md`](./COMMERCIAL.md) — the offline-license Enterprise
  Edition tier; reliability instrumentation is a *free-tier* feature
  because trust artifacts should be table stakes.
- [`skills.md`](./skills.md) — the sibling Phase-2.1 SOTA-parity
  work.
