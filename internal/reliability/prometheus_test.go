package reliability

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Regression tests for the PrometheusRecorder — the Phase-2.3
// half that feeds the /metrics endpoint. Every Dimension +
// SubMetric branch is asserted so a future refactor cannot
// silently drop a metric the operator's dashboard depends on.

// mustCollect grabs a metric's current value from a registry.
// Panics on registration errors — this is a test helper, not
// production code, and a panic makes the failure legible.
func mustCollect(t *testing.T, reg prometheus.Gatherer, name string) []*dto.Metric {
	t.Helper()
	families, err := reg.Gather()
	require.NoError(t, err)
	for _, mf := range families {
		if mf.GetName() == name {
			return mf.GetMetric()
		}
	}
	return nil
}

func TestPrometheusRecorder_TurnCounterByOutcome(t *testing.T) {
	reg := prometheus.NewRegistry()
	rec := NewPrometheusRecorder(reg)

	rec.Record(Sample{Dimension: DimSafety, SubMetric: "turn", Value: 1})
	rec.Record(Sample{Dimension: DimSafety, SubMetric: "turn", Value: 1})
	rec.Record(Sample{Dimension: DimSafety, SubMetric: "turn", Value: 0})

	metrics := mustCollect(t, reg, "rousseau_agent_requests_total")
	require.NotEmpty(t, metrics)

	counts := map[string]float64{}
	for _, m := range metrics {
		for _, l := range m.GetLabel() {
			if l.GetName() == "outcome" {
				counts[l.GetValue()] = m.GetCounter().GetValue()
			}
		}
	}
	assert.Equal(t, float64(2), counts["success"])
	assert.Equal(t, float64(1), counts["failure"])
}

func TestPrometheusRecorder_ViolationCounterLabels(t *testing.T) {
	reg := prometheus.NewRegistry()
	rec := NewPrometheusRecorder(reg)

	rec.Record(Sample{
		Dimension: DimSafety, SubMetric: "violation",
		Metadata: map[string]string{"constraint": "pii-detection", "severity": "high"},
	})
	rec.Record(Sample{
		Dimension: DimSafety, SubMetric: "violation",
		Metadata: map[string]string{"constraint": "pii-detection", "severity": "high"},
	})
	// A violation missing metadata should still emit — with
	// "unknown" labels rather than empty ones, which is
	// Prometheus best practice.
	rec.Record(Sample{Dimension: DimSafety, SubMetric: "violation"})

	metrics := mustCollect(t, reg, "rousseau_agent_safety_violations_total")
	require.Len(t, metrics, 2, "one series per (constraint, severity) tuple")

	byLabels := map[string]float64{}
	for _, m := range metrics {
		var constraint, severity string
		for _, l := range m.GetLabel() {
			switch l.GetName() {
			case "constraint":
				constraint = l.GetValue()
			case "severity":
				severity = l.GetValue()
			}
		}
		byLabels[constraint+"|"+severity] = m.GetCounter().GetValue()
	}
	assert.Equal(t, float64(2), byLabels["pii-detection|high"])
	assert.Equal(t, float64(1), byLabels["unknown|unknown"],
		"missing metadata defaults to `unknown` label values, never empty strings")
}

func TestPrometheusRecorder_LatencyHistogramInSeconds(t *testing.T) {
	reg := prometheus.NewRegistry()
	rec := NewPrometheusRecorder(reg)

	// Sample.Value is ms per the convention; histogram is in
	// seconds per Prometheus / OTel gen_ai naming.
	rec.Record(Sample{Dimension: DimConsistency, SubMetric: "resource_cv_latency", Value: 1500})
	rec.Record(Sample{Dimension: DimConsistency, SubMetric: "resource_cv_latency", Value: 500})

	metrics := mustCollect(t, reg, "rousseau_agent_request_duration_seconds")
	require.Len(t, metrics, 1, "unlabelled histogram = single series")
	h := metrics[0].GetHistogram()
	assert.Equal(t, uint64(2), h.GetSampleCount())
	// 1500 + 500 ms = 2.0 s.
	assert.InDelta(t, 2.0, h.GetSampleSum(), 1e-6)
}

func TestPrometheusRecorder_TokensAndCallsHistograms(t *testing.T) {
	reg := prometheus.NewRegistry()
	rec := NewPrometheusRecorder(reg)

	rec.Record(Sample{Dimension: DimConsistency, SubMetric: "resource_cv_tokens", Value: 500})
	rec.Record(Sample{Dimension: DimConsistency, SubMetric: "resource_cv_calls", Value: 3})

	tokens := mustCollect(t, reg, "rousseau_agent_request_tokens")
	require.Len(t, tokens, 1)
	assert.Equal(t, uint64(1), tokens[0].GetHistogram().GetSampleCount())
	assert.InDelta(t, 500, tokens[0].GetHistogram().GetSampleSum(), 1e-6)

	calls := mustCollect(t, reg, "rousseau_agent_request_tool_calls")
	require.Len(t, calls, 1)
	assert.InDelta(t, 3, calls[0].GetHistogram().GetSampleSum(), 1e-6)
}

func TestPrometheusRecorder_ConfidenceLabelledByOutcome(t *testing.T) {
	reg := prometheus.NewRegistry()
	rec := NewPrometheusRecorder(reg)

	rec.Record(Sample{
		Dimension: DimPredictability, SubMetric: "pair", Value: 0.9,
		Metadata: map[string]string{"outcome": "1"},
	})
	rec.Record(Sample{
		Dimension: DimPredictability, SubMetric: "pair", Value: 0.4,
		Metadata: map[string]string{"outcome": "0"},
	})

	metrics := mustCollect(t, reg, "rousseau_agent_confidence_score")
	require.Len(t, metrics, 2, "one series per outcome label")

	sums := map[string]float64{}
	for _, m := range metrics {
		for _, l := range m.GetLabel() {
			if l.GetName() == "outcome" {
				sums[l.GetValue()] = m.GetHistogram().GetSampleSum()
			}
		}
	}
	assert.InDelta(t, 0.9, sums["success"], 1e-6)
	assert.InDelta(t, 0.4, sums["failure"], 1e-6)
}

func TestPrometheusRecorder_FaultStratification(t *testing.T) {
	reg := prometheus.NewRegistry()
	rec := NewPrometheusRecorder(reg)

	// Two clean turns, both success.
	rec.Record(Sample{
		Dimension: DimRobustness, SubMetric: "fault", Value: 1,
		Metadata: map[string]string{"fault": "false"},
	})
	rec.Record(Sample{
		Dimension: DimRobustness, SubMetric: "fault", Value: 1,
		Metadata: map[string]string{"fault": "false"},
	})
	// One faulted turn, failure.
	rec.Record(Sample{
		Dimension: DimRobustness, SubMetric: "fault", Value: 0,
		Metadata: map[string]string{"fault": "true", "fault_kind": "timeout"},
	})

	faultTurns := mustCollect(t, reg, "rousseau_agent_fault_turns_total")
	require.Len(t, faultTurns, 2, "one series per (outcome, fault) tuple")

	byLabels := map[string]float64{}
	for _, m := range faultTurns {
		var outcome, fault string
		for _, l := range m.GetLabel() {
			switch l.GetName() {
			case "outcome":
				outcome = l.GetValue()
			case "fault":
				fault = l.GetValue()
			}
		}
		byLabels[outcome+"|"+fault] = m.GetCounter().GetValue()
	}
	assert.Equal(t, float64(2), byLabels["success|false"])
	assert.Equal(t, float64(1), byLabels["failure|true"])

	// Fault-kind counter only ticks for faulted turns.
	upstream := mustCollect(t, reg, "rousseau_agent_upstream_faults_total")
	require.Len(t, upstream, 1)
	assert.Equal(t, float64(1), upstream[0].GetCounter().GetValue())
	var kind string
	for _, l := range upstream[0].GetLabel() {
		if l.GetName() == "kind" {
			kind = l.GetValue()
		}
	}
	assert.Equal(t, "timeout", kind)
}

func TestPrometheusRecorder_UnknownDimensionsIgnored(t *testing.T) {
	// A future or bespoke dimension the recorder doesn't know
	// about must be silently dropped — never register a new
	// metric on the fly (unbounded cardinality risk).
	reg := prometheus.NewRegistry()
	rec := NewPrometheusRecorder(reg)

	assert.NotPanics(t, func() {
		rec.Record(Sample{Dimension: "made-up-dim", SubMetric: "x", Value: 1})
		rec.Record(Sample{Dimension: DimSafety, SubMetric: "made-up-sub", Value: 1})
	})

	// Nothing registered under the unknown dimension.
	metrics := mustCollect(t, reg, "rousseau_agent_made_up_dim")
	assert.Empty(t, metrics)
}

func TestPrometheusRecorder_NilRecorderIsSafe(t *testing.T) {
	var rec *PrometheusRecorder
	assert.NotPanics(t, func() {
		rec.Record(Sample{Dimension: DimSafety, SubMetric: "turn", Value: 1})
	})
}

func TestLabelOrUnknown(t *testing.T) {
	assert.Equal(t, "hi", labelOrUnknown(map[string]string{"k": "hi"}, "k"))
	assert.Equal(t, "unknown", labelOrUnknown(map[string]string{}, "k"))
	assert.Equal(t, "unknown", labelOrUnknown(nil, "k"))
	assert.Equal(t, "unknown", labelOrUnknown(map[string]string{"k": ""}, "k"))
}

// TestPrometheusRecorder_MetricsExposedViaHTTPFormat ensures the
// metrics emit in the shape Prometheus expects (HELP lines,
// counter values, histogram buckets). Full-text HTTP-endpoint
// tests live in the observability metrics_test.go suite; this
// one just proves the registry gather returns non-empty output.
func TestPrometheusRecorder_MetricsExposedViaHTTPFormat(t *testing.T) {
	reg := prometheus.NewRegistry()
	rec := NewPrometheusRecorder(reg)
	rec.Record(Sample{Dimension: DimSafety, SubMetric: "turn", Value: 1})

	families, err := reg.Gather()
	require.NoError(t, err)
	require.NotEmpty(t, families)

	// Prometheus text-exposition spot-check: at least one family
	// name should carry the rousseau_agent_ prefix per the naming
	// convention documented in docs/reliability.md.
	names := make([]string, 0, len(families))
	for _, f := range families {
		names = append(names, f.GetName())
	}
	assert.True(t, strings.HasPrefix(names[0], "rousseau_agent_"),
		"metric families must use the rousseau_agent_ prefix (got %v)", names)
}
