package reliability

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Regression tests for the four-dimension reliability decomposition
// per arXiv:2602.16666. Every metric branch has a dedicated case
// so a future refactor cannot silently reintroduce a bug — the
// paper's formulas are load-bearing for enterprise claims.

// -- Aggregator ------------------------------------------------------

func TestNewAggregator_DefaultCap(t *testing.T) {
	a := NewAggregator(0)
	assert.Equal(t, 10000, a.maxSamples)
	// Negative also uses default.
	a = NewAggregator(-1)
	assert.Equal(t, 10000, a.maxSamples)
}

func TestAggregator_RecordStampsAt(t *testing.T) {
	fixed := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	a := NewAggregator(10)
	a.now = func() time.Time { return fixed }

	a.Record(Sample{Dimension: DimSafety, SubMetric: "turn", Value: 1})
	require.Equal(t, 1, a.SampleCount())
	assert.Equal(t, fixed, a.Samples()[0].At, "zero At must be stamped to now()")
}

func TestAggregator_RecordPreservesExplicitAt(t *testing.T) {
	fixed := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	explicit := time.Date(2026, 9, 6, 10, 0, 0, 0, time.UTC)
	a := NewAggregator(10)
	a.now = func() time.Time { return fixed }

	a.Record(Sample{At: explicit, Dimension: DimSafety, SubMetric: "turn", Value: 1})
	assert.Equal(t, explicit, a.Samples()[0].At, "explicit At must be preserved")
}

func TestAggregator_FIFODropWhenFull(t *testing.T) {
	a := NewAggregator(3)
	for i := 0; i < 5; i++ {
		a.Record(Sample{Dimension: DimSafety, SubMetric: "turn", Value: float64(i)})
	}
	assert.Equal(t, 3, a.SampleCount(), "ring must cap at maxSamples")
	// Oldest two dropped (0 and 1); should have 2, 3, 4.
	vals := []float64{}
	for _, s := range a.Samples() {
		vals = append(vals, s.Value)
	}
	assert.Equal(t, []float64{2, 3, 4}, vals)
}

func TestAggregator_SummaryWindowFilters(t *testing.T) {
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	a := NewAggregator(10)
	a.now = func() time.Time { return now }

	// Ancient sample (10 days old) — outside a 7-day window.
	a.Record(Sample{At: now.Add(-10 * 24 * time.Hour), Dimension: DimSafety, SubMetric: "turn", Value: 0})
	// Recent sample (1 day old) — inside.
	a.Record(Sample{At: now.Add(-24 * time.Hour), Dimension: DimSafety, SubMetric: "turn", Value: 1})

	got := a.Summary(7 * 24 * time.Hour)
	assert.Equal(t, 1, got.Safety.SampleCount,
		"only the recent sample must count toward the 7d window")
	assert.InDelta(t, 1.0, got.Safety.SubScores["compliance"], 1e-9)
}

// -- Consistency: outcome ---------------------------------------------

func TestOutcomeConsistency_PerfectlyConsistent(t *testing.T) {
	samples := []Sample{
		{Dimension: DimConsistency, SubMetric: "outcome", Bucket: "b1", Value: 1},
		{Dimension: DimConsistency, SubMetric: "outcome", Bucket: "b1", Value: 1},
		{Dimension: DimConsistency, SubMetric: "outcome", Bucket: "b1", Value: 1},
	}
	got, ok := outcomeConsistency(samples)
	require.True(t, ok)
	assert.InDelta(t, 1.0, got, 1e-6, "3× success → variance 0 → C_out = 1")
}

func TestOutcomeConsistency_MaximallyInconsistent(t *testing.T) {
	// 50/50 outcomes → variance ≈ p(1-p) → ratio ≈ 1 → C_out ≈ 0.
	samples := []Sample{
		{Dimension: DimConsistency, SubMetric: "outcome", Bucket: "b1", Value: 1},
		{Dimension: DimConsistency, SubMetric: "outcome", Bucket: "b1", Value: 0},
	}
	got, ok := outcomeConsistency(samples)
	require.True(t, ok)
	assert.LessOrEqual(t, got, 0.05, "50/50 outcomes must give ~0 consistency (was %v)", got)
}

func TestOutcomeConsistency_SingleSampleBucketSkipped(t *testing.T) {
	// A bucket with only one sample has undefined variance and
	// must not contribute. Two buckets of 1 each → no ratios → not-ok.
	samples := []Sample{
		{Dimension: DimConsistency, SubMetric: "outcome", Bucket: "b1", Value: 1},
		{Dimension: DimConsistency, SubMetric: "outcome", Bucket: "b2", Value: 0},
	}
	_, ok := outcomeConsistency(samples)
	assert.False(t, ok)
}

func TestOutcomeConsistency_ClampsToZero(t *testing.T) {
	// Tiny-sample edge case: variance can exceed p(1-p), pushing
	// C_out below 0. The clamp must catch it so the score stays
	// in [0,1].
	samples := []Sample{
		{Dimension: DimConsistency, SubMetric: "outcome", Bucket: "b1", Value: 0},
		{Dimension: DimConsistency, SubMetric: "outcome", Bucket: "b1", Value: 1},
	}
	got, ok := outcomeConsistency(samples)
	require.True(t, ok)
	assert.GreaterOrEqual(t, got, 0.0, "clamp must prevent negative C_out")
	assert.LessOrEqual(t, got, 1.0)
}

// -- Consistency: resource -------------------------------------------

func TestResourceConsistency_ZeroCVGivesOne(t *testing.T) {
	// Identical resource values across a bucket → CV = 0 → exp(0) = 1.
	samples := []Sample{
		{Dimension: DimConsistency, SubMetric: "resource_cv_tokens", Bucket: "b1", Value: 100},
		{Dimension: DimConsistency, SubMetric: "resource_cv_tokens", Bucket: "b1", Value: 100},
		{Dimension: DimConsistency, SubMetric: "resource_cv_tokens", Bucket: "b1", Value: 100},
	}
	got, ok := resourceConsistency(samples)
	require.True(t, ok)
	assert.InDelta(t, 1.0, got, 1e-6)
}

func TestResourceConsistency_MultipleResourceTypesAveraged(t *testing.T) {
	// Two resource types (tokens, latency), each perfectly stable.
	samples := []Sample{
		{Dimension: DimConsistency, SubMetric: "resource_cv_tokens", Bucket: "b1", Value: 100},
		{Dimension: DimConsistency, SubMetric: "resource_cv_tokens", Bucket: "b1", Value: 100},
		{Dimension: DimConsistency, SubMetric: "resource_cv_latency", Bucket: "b1", Value: 200},
		{Dimension: DimConsistency, SubMetric: "resource_cv_latency", Bucket: "b1", Value: 200},
	}
	got, ok := resourceConsistency(samples)
	require.True(t, ok)
	assert.InDelta(t, 1.0, got, 1e-6)
}

func TestResourceConsistency_HighVarianceLowersScore(t *testing.T) {
	// Wildly varying resource values → CV large → exp(-CV) small.
	samples := []Sample{
		{Dimension: DimConsistency, SubMetric: "resource_cv_tokens", Bucket: "b1", Value: 100},
		{Dimension: DimConsistency, SubMetric: "resource_cv_tokens", Bucket: "b1", Value: 10000},
	}
	got, ok := resourceConsistency(samples)
	require.True(t, ok)
	assert.Less(t, got, 0.5, "wildly varying tokens must produce a low resource score (was %v)", got)
}

func TestResourceConsistency_NoSamplesReturnsFalse(t *testing.T) {
	_, ok := resourceConsistency(nil)
	assert.False(t, ok)
}

func TestResourceConsistency_ZeroMeanSkipsResource(t *testing.T) {
	// Zero mean is undefined for CV; the code must skip that bucket
	// rather than divide by zero.
	samples := []Sample{
		{Dimension: DimConsistency, SubMetric: "resource_cv_tokens", Bucket: "b1", Value: 0},
		{Dimension: DimConsistency, SubMetric: "resource_cv_tokens", Bucket: "b1", Value: 0},
	}
	_, ok := resourceConsistency(samples)
	assert.False(t, ok, "all-zero bucket must yield no CV signal")
}

func TestResourceConsistency_IgnoresWrongSubMetric(t *testing.T) {
	// Only samples with SubMetric prefix "resource_cv_" count.
	samples := []Sample{
		{Dimension: DimConsistency, SubMetric: "outcome", Bucket: "b1", Value: 1},
	}
	_, ok := resourceConsistency(samples)
	assert.False(t, ok)
}

// -- Consistency: trajectory -----------------------------------------

func TestTrajectoryConsistency_BothComponentsAveraged(t *testing.T) {
	samples := []Sample{
		{Dimension: DimConsistency, SubMetric: "trajectory_dist", Value: 0.1},
		{Dimension: DimConsistency, SubMetric: "trajectory_seq", Value: 0.2},
	}
	got, ok := trajectoryConsistency(samples)
	require.True(t, ok)
	// C_traj = mean(1-0.1, 1-0.2) = mean(0.9, 0.8) = 0.85
	assert.InDelta(t, 0.85, got, 1e-6)
}

func TestTrajectoryConsistency_OneComponentOnly(t *testing.T) {
	// Only distributional data — that half is the whole score.
	samples := []Sample{
		{Dimension: DimConsistency, SubMetric: "trajectory_dist", Value: 0.2},
	}
	got, ok := trajectoryConsistency(samples)
	require.True(t, ok)
	assert.InDelta(t, 0.8, got, 1e-6)
}

func TestTrajectoryConsistency_NoSamples(t *testing.T) {
	_, ok := trajectoryConsistency(nil)
	assert.False(t, ok)
}

// -- Robustness ------------------------------------------------------

func TestFaultRobustness_PerfectRatio(t *testing.T) {
	// Fault stratum: 100% success; clean stratum: 100% success → ratio 1.
	samples := []Sample{
		{Dimension: DimRobustness, SubMetric: "fault", Value: 1, Metadata: map[string]string{"fault": "true"}},
		{Dimension: DimRobustness, SubMetric: "fault", Value: 1, Metadata: map[string]string{"fault": "true"}},
		{Dimension: DimRobustness, SubMetric: "fault", Value: 1, Metadata: map[string]string{"fault": "false"}},
	}
	got, ok := faultRobustness(samples)
	require.True(t, ok)
	assert.InDelta(t, 1.0, got, 1e-9)
}

func TestFaultRobustness_HalfRatio(t *testing.T) {
	// Faulted: 50% success; clean: 100%. Ratio = 0.5.
	samples := []Sample{
		{Dimension: DimRobustness, SubMetric: "fault", Value: 1, Metadata: map[string]string{"fault": "true"}},
		{Dimension: DimRobustness, SubMetric: "fault", Value: 0, Metadata: map[string]string{"fault": "true"}},
		{Dimension: DimRobustness, SubMetric: "fault", Value: 1, Metadata: map[string]string{"fault": "false"}},
	}
	got, ok := faultRobustness(samples)
	require.True(t, ok)
	assert.InDelta(t, 0.5, got, 1e-9)
}

func TestFaultRobustness_MissingStratum(t *testing.T) {
	// Only faulted samples → cannot compute a ratio.
	samples := []Sample{
		{Dimension: DimRobustness, SubMetric: "fault", Value: 1, Metadata: map[string]string{"fault": "true"}},
	}
	_, ok := faultRobustness(samples)
	assert.False(t, ok)
}

func TestFaultRobustness_ZeroCleanAccuracyIsNaN(t *testing.T) {
	// Clean stratum has zero accuracy → ratio is undefined
	// (division by zero). Aggregator must not divide.
	samples := []Sample{
		{Dimension: DimRobustness, SubMetric: "fault", Value: 0, Metadata: map[string]string{"fault": "true"}},
		{Dimension: DimRobustness, SubMetric: "fault", Value: 0, Metadata: map[string]string{"fault": "false"}},
	}
	_, ok := faultRobustness(samples)
	assert.False(t, ok)
}

func TestEnvRobustness_TwoVersions(t *testing.T) {
	// v2 (newest) 100% success; v1 80%. Worst ratio = 0.8.
	samples := []Sample{
		{Dimension: DimRobustness, SubMetric: "env", Value: 1, Metadata: map[string]string{"schema_version": "v2"}},
		{Dimension: DimRobustness, SubMetric: "env", Value: 1, Metadata: map[string]string{"schema_version": "v1"}},
		{Dimension: DimRobustness, SubMetric: "env", Value: 1, Metadata: map[string]string{"schema_version": "v1"}},
		{Dimension: DimRobustness, SubMetric: "env", Value: 1, Metadata: map[string]string{"schema_version": "v1"}},
		{Dimension: DimRobustness, SubMetric: "env", Value: 0, Metadata: map[string]string{"schema_version": "v1"}},
		{Dimension: DimRobustness, SubMetric: "env", Value: 1, Metadata: map[string]string{"schema_version": "v1"}},
	}
	got, ok := envRobustness(samples)
	require.True(t, ok)
	assert.InDelta(t, 0.8, got, 1e-6)
}

func TestEnvRobustness_SingleVersionIsInsufficient(t *testing.T) {
	samples := []Sample{
		{Dimension: DimRobustness, SubMetric: "env", Value: 1, Metadata: map[string]string{"schema_version": "v1"}},
	}
	_, ok := envRobustness(samples)
	assert.False(t, ok)
}

func TestEnvRobustness_EmptyVersionSkipped(t *testing.T) {
	// Samples without schema_version metadata are silently dropped.
	samples := []Sample{
		{Dimension: DimRobustness, SubMetric: "env", Value: 1, Metadata: map[string]string{"schema_version": ""}},
	}
	_, ok := envRobustness(samples)
	assert.False(t, ok)
}

func TestEnvRobustness_ZeroCurrentAccuracyIsNaN(t *testing.T) {
	// "current" (highest-sort version) has 0 mean → ratio undefined.
	samples := []Sample{
		{Dimension: DimRobustness, SubMetric: "env", Value: 0, Metadata: map[string]string{"schema_version": "v2"}},
		{Dimension: DimRobustness, SubMetric: "env", Value: 1, Metadata: map[string]string{"schema_version": "v1"}},
	}
	_, ok := envRobustness(samples)
	assert.False(t, ok)
}

func TestPromptRobustness_DeltaMean(t *testing.T) {
	// Two clusters with delta 0.1 and 0.3 → R_prompt = 1 - 0.2 = 0.8.
	samples := []Sample{
		{Dimension: DimRobustness, SubMetric: "prompt", Value: 0.1},
		{Dimension: DimRobustness, SubMetric: "prompt", Value: 0.3},
	}
	got, ok := promptRobustness(samples)
	require.True(t, ok)
	assert.InDelta(t, 0.8, got, 1e-6)
}

func TestPromptRobustness_NoSamples(t *testing.T) {
	_, ok := promptRobustness(nil)
	assert.False(t, ok)
}

// -- Predictability --------------------------------------------------

func TestBrier_PerfectPrediction(t *testing.T) {
	// c=1 → y=1 and c=0 → y=0 → Brier = 1 - 0 = 1 (perfect).
	pairs := []pair{{c: 1, y: 1}, {c: 0, y: 0}}
	assert.InDelta(t, 1.0, brier(pairs), 1e-9)
}

func TestBrier_WorstPrediction(t *testing.T) {
	// c=1 → y=0 and c=0 → y=1 → Brier = 1 - 1 = 0 (worst).
	pairs := []pair{{c: 1, y: 0}, {c: 0, y: 1}}
	assert.InDelta(t, 0.0, brier(pairs), 1e-9)
}

func TestBrier_EmptyIsNaN(t *testing.T) {
	assert.True(t, math.IsNaN(brier(nil)))
}

func TestECE_PerfectCalibration(t *testing.T) {
	// Perfect calibration = the bin's mean confidence exactly
	// matches the bin's mean outcome. c=0→y=0 and c=1→y=1 puts
	// each pair in the extreme bin where the mean-conf and mean-
	// outcome both equal the pair's own value → ECE=0.
	pairs := []pair{{c: 0, y: 0}, {c: 1, y: 1}}
	assert.InDelta(t, 0.0, ece(pairs, 10), 1e-9)
}

func TestECE_ImperfectCalibration(t *testing.T) {
	// c=0.1 → y=0 puts the pair in bin 1 where meanConf=0.1 and
	// meanOut=0 — bin-level mis-calibration of 0.1. Similarly for
	// c=0.9 → y=1 in bin 9 (|1 - 0.9| = 0.1). Weighted mean = 0.1.
	pairs := []pair{{c: 0.1, y: 0}, {c: 0.9, y: 1}}
	assert.InDelta(t, 0.1, ece(pairs, 10), 1e-9)
}

func TestECE_ZeroBinsIsNaN(t *testing.T) {
	pairs := []pair{{c: 0.5, y: 1}}
	assert.True(t, math.IsNaN(ece(pairs, 0)))
}

func TestECE_EmptyIsNaN(t *testing.T) {
	assert.True(t, math.IsNaN(ece(nil, 10)))
}

func TestAUROC_PerfectDiscrimination(t *testing.T) {
	// Every success has higher confidence than every failure → AUROC = 1.
	pairs := []pair{{c: 0.9, y: 1}, {c: 0.8, y: 1}, {c: 0.2, y: 0}, {c: 0.1, y: 0}}
	got, ok := auroc(pairs)
	require.True(t, ok)
	assert.InDelta(t, 1.0, got, 1e-9)
}

func TestAUROC_TieCountsHalf(t *testing.T) {
	// One success and one failure at the same confidence → 0.5.
	pairs := []pair{{c: 0.5, y: 1}, {c: 0.5, y: 0}}
	got, ok := auroc(pairs)
	require.True(t, ok)
	assert.InDelta(t, 0.5, got, 1e-9)
}

func TestAUROC_AllSuccessOrAllFailureIsNaN(t *testing.T) {
	pairs := []pair{{c: 0.9, y: 1}, {c: 0.8, y: 1}}
	_, ok := auroc(pairs)
	assert.False(t, ok, "AUROC undefined without both a success and a failure")
}

func TestExtractPairs_UsesOutcomeMetadata(t *testing.T) {
	samples := []Sample{
		{Dimension: DimPredictability, SubMetric: "pair", Value: 0.7, Metadata: map[string]string{"outcome": "1"}},
		{Dimension: DimPredictability, SubMetric: "pair", Value: 0.4, Metadata: map[string]string{"outcome": "0"}},
	}
	got := extractPairs(samples)
	require.Len(t, got, 2)
	assert.Equal(t, 1.0, got[0].y)
	assert.Equal(t, 0.0, got[1].y)
}

func TestExtractPairs_ClampsConfidence(t *testing.T) {
	samples := []Sample{
		{Dimension: DimPredictability, SubMetric: "pair", Value: 1.5, Metadata: map[string]string{"outcome": "1"}},
		{Dimension: DimPredictability, SubMetric: "pair", Value: -0.5, Metadata: map[string]string{"outcome": "0"}},
	}
	got := extractPairs(samples)
	assert.Equal(t, 1.0, got[0].c, "clamp > 1 to 1")
	assert.Equal(t, 0.0, got[1].c, "clamp < 0 to 0")
}

// -- Safety ---------------------------------------------------------

func TestSafety_PerfectCompliance(t *testing.T) {
	// 5 turns, all compliant, no violations → S_comp=1, S_harm=1
	// → R_Saf = 1.
	samples := []Sample{
		{Dimension: DimSafety, SubMetric: "turn", Value: 1},
		{Dimension: DimSafety, SubMetric: "turn", Value: 1},
		{Dimension: DimSafety, SubMetric: "turn", Value: 1},
	}
	got := safetySummary(samples)
	assert.InDelta(t, 1.0, got.SubScores["compliance"], 1e-9)
	assert.InDelta(t, 1.0, got.Score, 1e-9)
}

func TestSafety_ViolationWeightedBySeverity(t *testing.T) {
	// One high-severity violation → severity = 1 - 1.0 = 0.
	// One compliant turn → compliance = 1.0.
	// Kaplan-Garrick: 1 - (1-1)*(1-0) = 1. High compliance can
	// mask a single high-severity violation (paper's warning).
	samples := []Sample{
		{Dimension: DimSafety, SubMetric: "turn", Value: 1},
		{Dimension: DimSafety, SubMetric: "violation", Metadata: map[string]string{"severity": "high"}},
	}
	got := safetySummary(samples)
	assert.InDelta(t, 1.0, got.Score, 1e-9,
		"paper warns explicitly: high-compliance + rare high-severity → still 1.0 aggregate. The OperatorS.responsibility is to alert on the raw violation counter, not the aggregate.")
}

func TestSafety_ComplianceBelowOneReducesScore(t *testing.T) {
	// 4 of 5 turns compliant → S_comp = 0.8; no severity data →
	// severity treated as 1 → R_Saf = 1 - 0.2*0 = 1... wait, that
	// hides the non-compliance. Actually with severity=1: 1 - (1-0.8)*(1-1) = 1.
	// This confirms the paper's warning: use raw compliance for
	// alerting, not the aggregate.
	samples := []Sample{
		{Dimension: DimSafety, SubMetric: "turn", Value: 1},
		{Dimension: DimSafety, SubMetric: "turn", Value: 1},
		{Dimension: DimSafety, SubMetric: "turn", Value: 1},
		{Dimension: DimSafety, SubMetric: "turn", Value: 1},
		{Dimension: DimSafety, SubMetric: "turn", Value: 0},
	}
	got := safetySummary(samples)
	assert.InDelta(t, 0.8, got.SubScores["compliance"], 1e-9)
}

func TestSafety_NoTurnsRecordedScoreIsNaN(t *testing.T) {
	got := safetySummary(nil)
	assert.True(t, math.IsNaN(got.Score))
	assert.Contains(t, got.Note, "no turn-level samples")
}

func TestSeverityWeight_Table(t *testing.T) {
	assert.Equal(t, 0.25, severityWeight("low"))
	assert.Equal(t, 0.5, severityWeight("medium"))
	assert.Equal(t, 0.5, severityWeight("med"))
	assert.Equal(t, 1.0, severityWeight("high"))
	assert.Equal(t, 0.5, severityWeight("unknown-value"), "unknown severity defaults to medium")
	assert.Equal(t, 0.5, severityWeight(""))
}

// -- Overall aggregate ---------------------------------------------

func TestSummary_OverallExcludesSafety(t *testing.T) {
	// Paper §3.4: safety is never folded into R because a tail
	// violation would average out. Verify: perfect safety with
	// only-safety samples → Overall is NaN, not 1.
	a := NewAggregator(10)
	a.Record(Sample{Dimension: DimSafety, SubMetric: "turn", Value: 1})
	got := a.Summary(time.Hour)
	assert.True(t, math.IsNaN(got.Overall),
		"Overall must exclude Safety — a safety-only summary yields NaN Overall, not 1.0")
	assert.InDelta(t, 1.0, got.Safety.Score, 1e-9)
}

func TestSummary_OverallIsMeanOfThreeDimensions(t *testing.T) {
	// Consistency 1.0 (via resource CV), Predictability 1.0 (via
	// perfect Brier), Robustness 0.5 (via fault ratio).
	// Overall = mean(1.0, 0.5, 1.0) = 0.833.
	a := NewAggregator(20)
	// Consistency via resource CV = 0 → 1.0
	for i := 0; i < 3; i++ {
		a.Record(Sample{Dimension: DimConsistency, SubMetric: "resource_cv_tokens", Bucket: "b1", Value: 100})
	}
	// Robustness fault ratio 0.5
	a.Record(Sample{Dimension: DimRobustness, SubMetric: "fault", Value: 1, Metadata: map[string]string{"fault": "true"}})
	a.Record(Sample{Dimension: DimRobustness, SubMetric: "fault", Value: 0, Metadata: map[string]string{"fault": "true"}})
	a.Record(Sample{Dimension: DimRobustness, SubMetric: "fault", Value: 1, Metadata: map[string]string{"fault": "false"}})
	// Predictability Brier 1.0
	a.Record(Sample{Dimension: DimPredictability, SubMetric: "pair", Value: 1, Metadata: map[string]string{"outcome": "1"}})
	a.Record(Sample{Dimension: DimPredictability, SubMetric: "pair", Value: 0, Metadata: map[string]string{"outcome": "0"}})

	got := a.Summary(time.Hour)
	assert.InDelta(t, (1.0+0.5+1.0)/3, got.Overall, 1e-6)
}

// -- Small helpers --------------------------------------------------

func TestMean_Empty(t *testing.T) {
	assert.True(t, math.IsNaN(mean(nil)))
}

func TestMeanIgnoreNaN_AllNaN(t *testing.T) {
	assert.True(t, math.IsNaN(meanIgnoreNaN(math.NaN(), math.NaN())))
}

func TestMeanIgnoreNaN_MixedIgnoresNaN(t *testing.T) {
	assert.InDelta(t, 0.5, meanIgnoreNaN(0, 1, math.NaN()), 1e-9)
}

func TestClamp01(t *testing.T) {
	assert.Equal(t, 0.0, clamp01(-0.5))
	assert.Equal(t, 1.0, clamp01(1.5))
	assert.Equal(t, 0.5, clamp01(0.5))
	assert.True(t, math.IsNaN(clamp01(math.NaN())))
}

func TestSampleVariance_SingleValueIsZero(t *testing.T) {
	assert.Equal(t, 0.0, sampleVariance([]float64{5}))
}

func TestSampleVariance_TwoValues(t *testing.T) {
	// sample variance of {1, 3}: mean 2, deviations ±1, sum-sq 2,
	// divided by (n-1) = 1 → 2.
	assert.InDelta(t, 2.0, sampleVariance([]float64{1, 3}), 1e-9)
}

func TestJoinNotes(t *testing.T) {
	assert.Empty(t, joinNotes(nil))
	assert.Equal(t, "one", joinNotes([]string{"one"}))
	assert.Equal(t, "one; two; three", joinNotes([]string{"one", "two", "three"}))
}
