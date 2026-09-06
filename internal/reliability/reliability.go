// Package reliability computes the four-dimension agent-reliability
// decomposition proposed in "Towards a Science of AI Agent
// Reliability" (Rabanser et al., arXiv:2602.16666, Feb 2026).
//
// The paper argues that a single "success rate" metric — the field's
// default in 2025 — obscures the reliability properties that
// enterprise buyers actually differentiate on: whether the agent
// gives the same answer twice, whether it withstands malformed
// input, whether it knows when it's uncertain, and whether it
// refuses actions it should refuse. Rousseau's implementation:
//
//   Dimension            Live-measurable        SubMetric family
//   -------------------  --------------------   -----------------
//   Consistency (R_Con)  Partially (resource    outcome, trajectory,
//                        CV is live; outcome    resource
//                        and trajectory need
//                        semantic bucketing)
//   Robustness (R_Rob)   Partially (fault      fault, env, prompt
//                        stratification is
//                        live; the rest is
//                        synthetic)
//   Predictability       Fully live (confidence  calibration, AUROC,
//   (R_Pred)             × outcome pairs)        Brier
//   Safety (R_Saf)       Fully live (violation   compliance, severity
//                        counters against
//                        a constraint set)
//
// The overall Reliability score `R = mean(R_Con, R_Rob, R_Pred)`
// deliberately EXCLUDES Safety per the paper — safety is a tail
// phenomenon that averaging out hides. Callers surface R_Saf next
// to R, never folded in.
//
// This package ships the metric primitives and the in-memory rolling
// aggregator. Persistent storage lives in the SQLite state layer
// (reliability_samples table). CLI surface: `rousseau reliability`.
// Wiring into agent.Turn / approver / transport is a follow-on
// commit — this package is a pure library addition.
//
// Full methodology, citations, and honest limitations documented at
// docs/reliability.md.
package reliability

import (
	"math"
	"sort"
	"sync"
	"time"
)

// Dimension names the four reliability axes. Values match the paper's
// R_Con / R_Rob / R_Pred / R_Saf and the corresponding Prometheus
// metric names (`rousseau_agent_consistency_score`, etc).
type Dimension string

// Enumerated dimensions. Callers should never build a Dimension
// value from a string literal — use these constants so a typo fails
// at compile time.
const (
	DimConsistency    Dimension = "consistency"
	DimRobustness     Dimension = "robustness"
	DimPredictability Dimension = "predictability"
	DimSafety         Dimension = "safety"
)

// AllDimensions is the canonical iteration order for reports — the
// order the paper presents them and the order the CLI renders them.
// Not sorted alphabetically because the operator-facing order should
// match reader expectations, not lexicographic.
var AllDimensions = []Dimension{
	DimConsistency,
	DimRobustness,
	DimPredictability,
	DimSafety,
}

// Sample is one observation the recorder stores. Every field except
// Value is optional — the aggregator degrades gracefully when
// SessionID / Bucket / Metadata are empty.
//
// SubMetric names the specific sub-score inside the Dimension. For
// Consistency: "outcome" | "trajectory_dist" | "trajectory_seq" |
// "resource_cv_tokens" | "resource_cv_latency" | "resource_cv_calls".
// For Robustness: "fault" | "env" | "prompt". For Predictability:
// "brier" | "calibration" | "auroc" (or the raw pair "confidence"
// paired with "outcome" for on-the-fly Brier). For Safety:
// "violation" (with severity in Metadata) or "compliance".
//
// Interpretation of Value depends on SubMetric and follows the paper:
//   - success/outcome samples: 0.0 or 1.0
//   - confidence: 0.0..1.0
//   - resource observations: raw value (token count, milliseconds)
//   - violation severity: 0.25 (low), 0.5 (medium), 1.0 (high)
type Sample struct {
	At        time.Time
	Dimension Dimension
	SubMetric string
	Value     float64
	// SessionID lets the aggregator bucket consistency samples by
	// conversation. Empty when the recorder doesn't have one
	// (e.g. daemon-level events).
	SessionID string
	// Bucket is an opaque grouping key used by consistency
	// calculations — a hash of (route, tool signature,
	// normalised prompt embedding) so semantically-similar
	// requests contribute to the same variance calculation.
	// Empty means "global bucket".
	Bucket string
	// Metadata carries dimension-specific extra fields. Safety
	// uses `severity` ("low"|"medium"|"high") and `constraint`
	// (rule id). Robustness uses `fault_kind` ("timeout"|"schema"|
	// "auth"|"other"). Kept as a plain string map so the SQLite
	// serialisation is JSON-trivial.
	Metadata map[string]string
}

// Aggregator holds a rolling in-memory buffer of samples and
// computes per-dimension scores over configurable windows.
//
// Safe for concurrent use — every method takes an internal mutex.
// The buffer is capped by MaxSamples to bound memory; the oldest
// samples are dropped first (FIFO). Persistent storage of samples
// beyond the buffer lives in state/sqlite (see the reliability
// store in that package).
type Aggregator struct {
	mu         sync.RWMutex
	samples    []Sample
	maxSamples int
	now        func() time.Time // injectable clock for tests
}

// NewAggregator returns an Aggregator with the given ring cap.
// max ≤ 0 uses a sensible default (10 000 samples, roughly a
// day at 10 turns per minute across all dimensions).
func NewAggregator(max int) *Aggregator {
	if max <= 0 {
		max = 10000
	}
	return &Aggregator{
		samples:    make([]Sample, 0, max),
		maxSamples: max,
		now:        time.Now,
	}
}

// Record adds a Sample to the buffer, dropping the oldest when the
// buffer is full. Sample.At is stamped to now() when zero — most
// callers pass an already-stamped sample from wherever the event
// originally fired.
func (a *Aggregator) Record(s Sample) {
	if s.At.IsZero() {
		s.At = a.now()
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.samples) >= a.maxSamples {
		// FIFO drop. copy is O(n) but the cap is small; for
		// higher throughputs migrate to a proper ring buffer.
		copy(a.samples, a.samples[1:])
		a.samples = a.samples[:len(a.samples)-1]
	}
	a.samples = append(a.samples, s)
}

// Samples returns a snapshot copy of every sample currently in the
// buffer. Intended for the reliability CLI / debug dumps. Returns
// a fresh slice so callers can sort / filter without affecting the
// aggregator.
func (a *Aggregator) Samples() []Sample {
	a.mu.RLock()
	defer a.mu.RUnlock()
	out := make([]Sample, len(a.samples))
	copy(out, a.samples)
	return out
}

// SampleCount returns the number of samples currently buffered.
// Useful for tests + CLI headers ("N samples over 7 days").
func (a *Aggregator) SampleCount() int {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return len(a.samples)
}

// Summary computes the four-dimension summary over samples with
// At in [now - window, now]. The aggregate Overall excludes Safety
// per the paper's guidance ("safety is a tail phenomenon; a 99%
// safe agent that catastrophically fails 1% of the time should
// never average out").
func (a *Aggregator) Summary(window time.Duration) Summary {
	a.mu.RLock()
	defer a.mu.RUnlock()
	cutoff := a.now().Add(-window)
	windowed := make([]Sample, 0, len(a.samples))
	for _, s := range a.samples {
		if s.At.After(cutoff) || s.At.Equal(cutoff) {
			windowed = append(windowed, s)
		}
	}
	return computeSummary(windowed, window)
}

// Summary is the four-dimension decomposition for a rolling window.
// Aggregate Overall = mean(Consistency, Robustness, Predictability).
// Safety is intentionally excluded from Overall — see paper §3.4.
type Summary struct {
	Window         time.Duration
	SampleCount    int
	Consistency    DimensionSummary
	Robustness     DimensionSummary
	Predictability DimensionSummary
	Safety         DimensionSummary
	// Overall is the paper's R = mean(R_Con, R_Rob, R_Pred).
	// NaN when fewer than 1 of the three has data — refuse to
	// invent a score from air.
	Overall float64
}

// DimensionSummary is one axis's aggregate + sub-scores. Score is
// the aggregate for the dimension (R_Con / R_Rob / R_Pred / R_Saf);
// SubScores holds every named sub-metric that had at least one
// sample, so the CLI / dashboard can drill down. NaN Score means
// "not enough data" and callers should render as "n/a" rather than
// zero (which would incorrectly claim the dimension is failing).
type DimensionSummary struct {
	Dimension   Dimension
	Score       float64
	SubScores   map[string]float64
	SampleCount int
	// Note lets the aggregator explain a NaN or a suspicious low
	// score to the operator: "no confidence signal recorded",
	// "insufficient consistency buckets (need ≥2 repeats per
	// bucket)", etc. Empty when the score is fully computed.
	Note string
}

// computeSummary is the pure calculation core — extracted from
// Summary so tests can drive it with a hand-picked sample slice
// without touching the aggregator's ring buffer.
func computeSummary(samples []Sample, window time.Duration) Summary {
	out := Summary{
		Window:         window,
		SampleCount:    len(samples),
		Consistency:    consistencySummary(filterDim(samples, DimConsistency)),
		Robustness:     robustnessSummary(filterDim(samples, DimRobustness)),
		Predictability: predictabilitySummary(filterDim(samples, DimPredictability)),
		Safety:         safetySummary(filterDim(samples, DimSafety)),
	}
	out.Overall = meanIgnoreNaN(out.Consistency.Score, out.Robustness.Score, out.Predictability.Score)
	return out
}

func filterDim(samples []Sample, dim Dimension) []Sample {
	out := make([]Sample, 0, len(samples)/4)
	for _, s := range samples {
		if s.Dimension == dim {
			out = append(out, s)
		}
	}
	return out
}

// -- Consistency ------------------------------------------------------

// consistencySummary computes R_Con = mean(C_out, C_traj, C_res)
// from the "consistency"-dimensioned samples. Each sub-score is
// computed independently; NaN means "no data" and is excluded from
// the mean (with a note explaining what's missing).
//
// C_out per paper: 1 − sample_variance(y) / (p·(1−p) + ε), bucketed
// by SessionID+Bucket so requests to the same route/tool contribute
// to the same variance. Needs ≥2 samples per bucket.
//
// C_res per paper: exp(−mean CV) over resource observations (tokens,
// latency, tool-calls). Live-measurable from every request.
//
// C_traj: mean(1 − distributional_divergence, 1 − sequential_edit).
// Requires action-sequence samples the daemon can emit per turn.
func consistencySummary(samples []Sample) DimensionSummary {
	subs := map[string]float64{}
	var notes []string

	// Each sub-score holds NaN when its inputs are missing, so
	// meanIgnoreNaN correctly excludes it from the aggregate.
	// (A missing map key returns 0.0, not NaN, which would
	// incorrectly pull the mean toward zero — the reason
	// TestSummary_OverallIsMeanOfThreeDimensions failed pre-fix.)
	outcome := math.NaN()
	if v, ok := outcomeConsistency(samples); ok {
		outcome = v
		subs["outcome"] = v
	} else {
		notes = append(notes, "outcome consistency: no bucket has ≥2 repeat samples")
	}

	// Resource consistency: exp(-mean CV) across each resource type.
	resource := math.NaN()
	if v, ok := resourceConsistency(samples); ok {
		resource = v
		subs["resource"] = v
	} else {
		notes = append(notes, "resource consistency: no resource_cv_* samples recorded")
	}

	// Trajectory consistency: pre-computed samples ("trajectory_dist",
	// "trajectory_seq") are averaged directly. Deferred until the
	// wiring commit emits action-sequence signals.
	trajectory := math.NaN()
	if v, ok := trajectoryConsistency(samples); ok {
		trajectory = v
		subs["trajectory"] = v
	} else {
		notes = append(notes, "trajectory consistency: no trajectory_* samples recorded")
	}

	score := meanIgnoreNaN(outcome, trajectory, resource)
	return DimensionSummary{
		Dimension:   DimConsistency,
		Score:       score,
		SubScores:   subs,
		SampleCount: len(samples),
		Note:        joinNotes(notes),
	}
}

func outcomeConsistency(samples []Sample) (float64, bool) {
	buckets := map[string][]float64{}
	for _, s := range samples {
		if s.SubMetric != "outcome" {
			continue
		}
		buckets[s.Bucket] = append(buckets[s.Bucket], s.Value)
	}
	// Only buckets with ≥2 samples contribute — variance of one
	// value is undefined.
	var ratios []float64
	for _, vs := range buckets {
		if len(vs) < 2 {
			continue
		}
		p := mean(vs)
		v := sampleVariance(vs)
		denom := p*(1-p) + 1e-8
		ratios = append(ratios, v/denom)
	}
	if len(ratios) == 0 {
		return math.NaN(), false
	}
	// C_out = 1 − mean(ratio); clamp to [0,1] because sample
	// variance can exceed p(1-p) on very small samples.
	c := 1 - mean(ratios)
	return clamp01(c), true
}

func resourceConsistency(samples []Sample) (float64, bool) {
	// Group by SubMetric prefix "resource_cv_" and by Bucket, then
	// average the coefficient-of-variation over resource types.
	// Each Sample.Value is a raw resource observation (tokens,
	// milliseconds, etc); the CV is computed inside this function.
	resources := map[string][]float64{}
	for _, s := range samples {
		if len(s.SubMetric) < len("resource_cv_") {
			continue
		}
		if s.SubMetric[:len("resource_cv_")] != "resource_cv_" {
			continue
		}
		key := s.SubMetric[len("resource_cv_"):] + "|" + s.Bucket
		resources[key] = append(resources[key], s.Value)
	}
	var cvs []float64
	for _, vs := range resources {
		if len(vs) < 2 {
			continue
		}
		m := mean(vs)
		if m == 0 {
			continue // no signal; CV undefined
		}
		sd := stddev(vs)
		cvs = append(cvs, sd/m)
	}
	if len(cvs) == 0 {
		return math.NaN(), false
	}
	// C_res = exp(-mean CV) per paper §3.1.
	return math.Exp(-mean(cvs)), true
}

func trajectoryConsistency(samples []Sample) (float64, bool) {
	var dist, seq []float64
	for _, s := range samples {
		switch s.SubMetric {
		case "trajectory_dist":
			dist = append(dist, s.Value)
		case "trajectory_seq":
			seq = append(seq, s.Value)
		}
	}
	if len(dist) == 0 && len(seq) == 0 {
		return math.NaN(), false
	}
	// Per paper §3.1: C_traj = ½(C_traj^d + C_traj^s); when only
	// one component has data, that half is the whole score.
	var parts []float64
	if len(dist) > 0 {
		parts = append(parts, 1-mean(dist))
	}
	if len(seq) > 0 {
		parts = append(parts, 1-mean(seq))
	}
	return clamp01(mean(parts)), true
}

// -- Robustness -------------------------------------------------------

// robustnessSummary computes R_Rob = mean(R_fault, R_env, R_prompt).
// Each sub-score is a ratio min(Acc_perturbed / Acc_baseline, 1).
// Recorder emits the raw success/fault-strata; the aggregator
// computes the ratio here so the samples are minimally structured.
func robustnessSummary(samples []Sample) DimensionSummary {
	subs := map[string]float64{}
	var notes []string

	// Same NaN-defaults pattern as consistencySummary — missing
	// map keys returning 0.0 would poison the aggregate.
	fault := math.NaN()
	if v, ok := faultRobustness(samples); ok {
		fault = v
		subs["fault"] = v
	} else {
		notes = append(notes, "fault robustness: need samples in both fault and clean strata")
	}
	env := math.NaN()
	if v, ok := envRobustness(samples); ok {
		env = v
		subs["env"] = v
	} else {
		notes = append(notes, "env robustness: need samples across two schema versions")
	}
	prompt := math.NaN()
	if v, ok := promptRobustness(samples); ok {
		prompt = v
		subs["prompt"] = v
	} else {
		notes = append(notes, "prompt robustness: need paraphrase clusters (synthetic; see docs/reliability.md)")
	}

	score := meanIgnoreNaN(fault, env, prompt)
	return DimensionSummary{
		Dimension:   DimRobustness,
		Score:       score,
		SubScores:   subs,
		SampleCount: len(samples),
		Note:        joinNotes(notes),
	}
}

func faultRobustness(samples []Sample) (float64, bool) {
	// Stratify by Metadata["fault"] = "true" vs anything else, then
	// take Acc_faulted / Acc_clean, clamped to [0,1].
	var faulted, clean []float64
	for _, s := range samples {
		if s.SubMetric != "fault" {
			continue
		}
		if s.Metadata["fault"] == "true" {
			faulted = append(faulted, s.Value)
		} else {
			clean = append(clean, s.Value)
		}
	}
	if len(faulted) == 0 || len(clean) == 0 {
		return math.NaN(), false
	}
	accClean := mean(clean)
	if accClean == 0 {
		return math.NaN(), false
	}
	return clamp01(mean(faulted) / accClean), true
}

func envRobustness(samples []Sample) (float64, bool) {
	// Stratify by Metadata["schema_version"] — expect exactly two
	// distinct values (current + previous) during a rollout window.
	byVersion := map[string][]float64{}
	for _, s := range samples {
		if s.SubMetric != "env" {
			continue
		}
		v := s.Metadata["schema_version"]
		if v == "" {
			continue
		}
		byVersion[v] = append(byVersion[v], s.Value)
	}
	if len(byVersion) < 2 {
		return math.NaN(), false
	}
	// With N>2 versions, take the min accuracy over the current
	// vs each previous — worst-case robustness across rollouts.
	// Sort keys for deterministic output.
	keys := make([]string, 0, len(byVersion))
	for k := range byVersion {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	first := mean(byVersion[keys[len(keys)-1]]) // "current" = highest-sort
	if first == 0 {
		return math.NaN(), false
	}
	worst := 1.0
	for _, k := range keys[:len(keys)-1] {
		if r := mean(byVersion[k]) / first; r < worst {
			worst = r
		}
	}
	return clamp01(worst), true
}

func promptRobustness(samples []Sample) (float64, bool) {
	// Pre-computed samples per cluster: SubMetric == "prompt",
	// Value is delta_acc for that cluster (0 = perfectly robust).
	// R_prompt = 1 − mean(delta_acc). Live implementation requires
	// paraphrase-clustering upstream; deferred to wiring commit.
	var deltas []float64
	for _, s := range samples {
		if s.SubMetric == "prompt" {
			deltas = append(deltas, s.Value)
		}
	}
	if len(deltas) == 0 {
		return math.NaN(), false
	}
	return clamp01(1 - mean(deltas)), true
}

// -- Predictability ---------------------------------------------------

// predictabilitySummary computes calibration (1 − ECE), discrimination
// (AUROC), and Brier score from paired (confidence, outcome) samples.
// The paper uses Brier as the aggregate R_Pred (proper scoring rule
// jointly penalising both).
//
// Sample convention: SubMetric == "pair", Value = confidence ∈ [0,1],
// Metadata["outcome"] = "1" or "0". Alternative sub-metrics allow
// callers that pre-compute the aggregates to skip the online math.
func predictabilitySummary(samples []Sample) DimensionSummary {
	subs := map[string]float64{}
	var notes []string

	pairs := extractPairs(samples)
	if len(pairs) > 0 {
		subs["brier"] = brier(pairs)
		subs["calibration"] = 1 - ece(pairs, 10)
		if auc, ok := auroc(pairs); ok {
			subs["auroc"] = auc
		}
	} else {
		notes = append(notes, "predictability: no (confidence, outcome) pairs recorded")
	}

	// Paper's R_Pred = Brier (as opposed to a mean of the three).
	// Fall back to mean of whatever we have if Brier is unavailable.
	score := math.NaN()
	if v, ok := subs["brier"]; ok {
		score = v
	} else if len(subs) > 0 {
		vals := make([]float64, 0, len(subs))
		for _, v := range subs {
			vals = append(vals, v)
		}
		score = mean(vals)
	}

	return DimensionSummary{
		Dimension:   DimPredictability,
		Score:       score,
		SubScores:   subs,
		SampleCount: len(samples),
		Note:        joinNotes(notes),
	}
}

// pair packages one (confidence, outcome) observation for the
// calibration / AUROC / Brier calculations.
type pair struct {
	c float64 // confidence ∈ [0,1]
	y float64 // outcome ∈ {0,1}
}

func extractPairs(samples []Sample) []pair {
	out := make([]pair, 0, len(samples))
	for _, s := range samples {
		if s.SubMetric != "pair" {
			continue
		}
		y := 0.0
		if s.Metadata["outcome"] == "1" {
			y = 1.0
		}
		out = append(out, pair{c: clamp01(s.Value), y: y})
	}
	return out
}

func brier(pairs []pair) float64 {
	if len(pairs) == 0 {
		return math.NaN()
	}
	sum := 0.0
	for _, p := range pairs {
		d := p.c - p.y
		sum += d * d
	}
	// Paper: P_brier = 1 − (1/N)·Σ(c − y)² — higher is better.
	return 1 - sum/float64(len(pairs))
}

func ece(pairs []pair, bins int) float64 {
	if len(pairs) == 0 || bins <= 0 {
		return math.NaN()
	}
	binCount := make([]int, bins)
	binConfSum := make([]float64, bins)
	binOutSum := make([]float64, bins)
	for _, p := range pairs {
		b := int(p.c * float64(bins))
		if b >= bins {
			b = bins - 1
		}
		binCount[b]++
		binConfSum[b] += p.c
		binOutSum[b] += p.y
	}
	total := float64(len(pairs))
	ece := 0.0
	for i := 0; i < bins; i++ {
		if binCount[i] == 0 {
			continue
		}
		meanConf := binConfSum[i] / float64(binCount[i])
		meanOut := binOutSum[i] / float64(binCount[i])
		ece += (float64(binCount[i]) / total) * math.Abs(meanOut-meanConf)
	}
	return ece
}

func auroc(pairs []pair) (float64, bool) {
	// Split into success and failure; count fraction of
	// (success, failure) pairs where success's confidence is
	// higher. Ties count 0.5.
	var succ, fail []float64
	for _, p := range pairs {
		if p.y >= 0.5 {
			succ = append(succ, p.c)
		} else {
			fail = append(fail, p.c)
		}
	}
	if len(succ) == 0 || len(fail) == 0 {
		return math.NaN(), false
	}
	greater := 0.0
	for _, s := range succ {
		for _, f := range fail {
			switch {
			case s > f:
				greater += 1
			case s == f:
				greater += 0.5
			}
		}
	}
	return greater / (float64(len(succ)) * float64(len(fail))), true
}

// -- Safety ---------------------------------------------------------

// safetySummary computes S_comp (compliance rate) and S_harm (harm
// severity) and combines them per Kaplan–Garrick risk formulation:
// R_Saf = 1 − (1 − S_comp)(1 − S_harm).
//
// Two sample shapes:
//   - "turn" samples: one per completed turn, Value = 1 (compliant)
//     or 0 (violated).
//   - "violation" samples: one per violation, Metadata["severity"]
//     ∈ {"low","medium","high"} → weight 0.25/0.5/1.0.
func safetySummary(samples []Sample) DimensionSummary {
	subs := map[string]float64{}
	var notes []string

	// Compliance = mean of "turn" samples (fraction fully compliant).
	var turns []float64
	for _, s := range samples {
		if s.SubMetric == "turn" {
			turns = append(turns, s.Value)
		}
	}
	if len(turns) > 0 {
		subs["compliance"] = mean(turns)
	} else {
		notes = append(notes, "compliance: no turn-level samples recorded")
	}

	// Severity = 1 − weighted mean of violation severities.
	var weights []float64
	for _, s := range samples {
		if s.SubMetric != "violation" {
			continue
		}
		w := severityWeight(s.Metadata["severity"])
		weights = append(weights, w)
	}
	if len(weights) > 0 {
		subs["severity"] = 1 - mean(weights)
	}

	// Kaplan–Garrick combination. When compliance is unknown fall
	// back to "unable to compute overall" (NaN) rather than
	// pretending violations mean nothing.
	score := math.NaN()
	if c, ok := subs["compliance"]; ok {
		s := subs["severity"]
		if math.IsNaN(s) {
			s = 1 // no violations recorded → severity component is 1
		}
		score = 1 - (1-c)*(1-s)
	}

	return DimensionSummary{
		Dimension:   DimSafety,
		Score:       score,
		SubScores:   subs,
		SampleCount: len(samples),
		Note:        joinNotes(notes),
	}
}

func severityWeight(sev string) float64 {
	switch sev {
	case "low":
		return 0.25
	case "medium", "med":
		return 0.5
	case "high":
		return 1.0
	default:
		// Unknown severity treated as medium to prevent a
		// mis-labelled violation from suppressing itself.
		return 0.5
	}
}

// -- Numeric helpers -------------------------------------------------

func mean(xs []float64) float64 {
	if len(xs) == 0 {
		return math.NaN()
	}
	sum := 0.0
	for _, x := range xs {
		sum += x
	}
	return sum / float64(len(xs))
}

func sampleVariance(xs []float64) float64 {
	if len(xs) < 2 {
		return 0
	}
	m := mean(xs)
	sum := 0.0
	for _, x := range xs {
		d := x - m
		sum += d * d
	}
	// Bessel-corrected sample variance (n−1).
	return sum / float64(len(xs)-1)
}

func stddev(xs []float64) float64 {
	return math.Sqrt(sampleVariance(xs))
}

func meanIgnoreNaN(vals ...float64) float64 {
	sum := 0.0
	count := 0
	for _, v := range vals {
		if math.IsNaN(v) {
			continue
		}
		sum += v
		count++
	}
	if count == 0 {
		return math.NaN()
	}
	return sum / float64(count)
}

func clamp01(x float64) float64 {
	switch {
	case math.IsNaN(x):
		return x
	case x < 0:
		return 0
	case x > 1:
		return 1
	default:
		return x
	}
}

func joinNotes(notes []string) string {
	if len(notes) == 0 {
		return ""
	}
	out := notes[0]
	for _, n := range notes[1:] {
		out += "; " + n
	}
	return out
}
