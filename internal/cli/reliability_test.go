package cli

import (
	"bytes"
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/reliability"
)

// Regression tests for the `rousseau reliability` CLI: parse the
// window flag, load the aggregator (synthetic vs real), render
// human + JSON.

// -- parseReliabilityWindow ------------------------------------------

func TestParseReliabilityWindow_TableDriven(t *testing.T) {
	cases := []struct {
		in      string
		want    time.Duration
		wantErr bool
	}{
		{"1d", 24 * time.Hour, false},
		{"7d", 7 * 24 * time.Hour, false},
		{"30d", 30 * 24 * time.Hour, false},
		{"90d", 90 * 24 * time.Hour, false},
		{"24h", 24 * time.Hour, false},
		{"90m", 90 * time.Minute, false},
		{"junk", 0, true},
		{"", 0, true},
		{"3.5d", 0, true}, // fractional-d not supported
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, err := parseReliabilityWindow(tc.in)
			if tc.wantErr {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// -- loadReliabilityAggregator ---------------------------------------

func TestLoadReliabilityAggregator_EmptyByDefault(t *testing.T) {
	agg := loadReliabilityAggregator(&Options{}, false)
	assert.NotNil(t, agg)
	assert.Zero(t, agg.SampleCount(), "no synthetic + no wired store → empty aggregator")
}

func TestLoadReliabilityAggregator_SyntheticPopulates(t *testing.T) {
	agg := loadReliabilityAggregator(&Options{}, true)
	assert.NotNil(t, agg)
	// Synthetic dataset spans all four dimensions with 300+ samples.
	assert.Greater(t, agg.SampleCount(), 200)

	// Sanity-check every dimension has at least one sample so the
	// synthetic set actually exercises the render code paths.
	dims := map[reliability.Dimension]int{}
	for _, s := range agg.Samples() {
		dims[s.Dimension]++
	}
	assert.Positive(t, dims[reliability.DimConsistency])
	assert.Positive(t, dims[reliability.DimRobustness])
	assert.Positive(t, dims[reliability.DimPredictability])
	assert.Positive(t, dims[reliability.DimSafety])
}

// -- renderReliability: human mode -----------------------------------

func TestRenderReliabilityHuman_NoSamplesMessage(t *testing.T) {
	var buf bytes.Buffer
	summary := reliability.NewAggregator(0).Summary(7 * 24 * time.Hour)
	require.NoError(t, renderReliability(&buf, summary, false))

	out := buf.String()
	assert.Contains(t, out, "no samples recorded yet")
	assert.Contains(t, out, "docs/reliability.md")
	assert.Contains(t, out, "--synthetic")
}

func TestRenderReliabilityHuman_WithSyntheticData(t *testing.T) {
	agg := loadReliabilityAggregator(&Options{}, true)
	summary := agg.Summary(7 * 24 * time.Hour)

	var buf bytes.Buffer
	require.NoError(t, renderReliability(&buf, summary, false))

	out := buf.String()
	// Header + Overall must be present.
	assert.Contains(t, out, "Reliability over 7d")
	assert.Contains(t, out, "Overall")
	// Every dimension gets its own section.
	assert.Contains(t, out, "consistency")
	assert.Contains(t, out, "robustness")
	assert.Contains(t, out, "predictability")
	// Safety marker calls out the "reported separately" rule per
	// arXiv:2602.16666 §3.4 — critical guardrail against a reader
	// treating safety as part of Overall.
	assert.Contains(t, out, "Safety (reported separately")
	assert.Contains(t, out, "never folded into Overall")
}

func TestRenderReliabilityHuman_ScoreFormatsPercentage(t *testing.T) {
	// A canned summary with a known aggregate → verify the percentage
	// renders to one decimal (spec: `%5.1f%%`).
	summary := reliability.Summary{
		Window:      7 * 24 * time.Hour,
		SampleCount: 1,
		Consistency: reliability.DimensionSummary{
			Dimension: reliability.DimConsistency, Score: 0.847, SampleCount: 1,
		},
		Robustness:     reliability.DimensionSummary{Dimension: reliability.DimRobustness, Score: 0.9, SampleCount: 1},
		Predictability: reliability.DimensionSummary{Dimension: reliability.DimPredictability, Score: 0.75, SampleCount: 1},
		Safety:         reliability.DimensionSummary{Dimension: reliability.DimSafety, Score: 1.0, SampleCount: 1},
		Overall:        0.832,
	}
	var buf bytes.Buffer
	require.NoError(t, renderReliability(&buf, summary, false))
	assert.Contains(t, buf.String(), " 83.2%", "Overall must render to one decimal")
	assert.Contains(t, buf.String(), " 84.7%")
	assert.Contains(t, buf.String(), "100.0%")
}

func TestRenderReliabilityHuman_NaNShownAsNa(t *testing.T) {
	summary := reliability.Summary{
		Window: 7 * 24 * time.Hour, SampleCount: 1,
		Consistency: reliability.DimensionSummary{
			Dimension: reliability.DimConsistency, Score: math.NaN(), SampleCount: 0,
			Note: "outcome consistency: no bucket has ≥2 repeat samples",
		},
		Robustness:     reliability.DimensionSummary{Dimension: reliability.DimRobustness, Score: math.NaN()},
		Predictability: reliability.DimensionSummary{Dimension: reliability.DimPredictability, Score: math.NaN()},
		Safety:         reliability.DimensionSummary{Dimension: reliability.DimSafety, Score: math.NaN()},
		Overall:        math.NaN(),
	}
	var buf bytes.Buffer
	require.NoError(t, renderReliability(&buf, summary, false))
	out := buf.String()
	assert.Contains(t, out, "n/a", "NaN scores render as 'n/a' rather than misleading 0.0%")
	assert.Contains(t, out, "outcome consistency", "Note must surface so operators know why the score is n/a")
}

func TestRenderReliabilityHuman_SubScoresRenderedInOrder(t *testing.T) {
	summary := reliability.Summary{
		Window: 7 * 24 * time.Hour, SampleCount: 3,
		Consistency: reliability.DimensionSummary{
			Dimension: reliability.DimConsistency, Score: 0.9, SampleCount: 3,
			SubScores: map[string]float64{"resource": 0.9, "outcome": 0.85, "trajectory": 0.95},
		},
		Robustness:     reliability.DimensionSummary{Dimension: reliability.DimRobustness, Score: math.NaN()},
		Predictability: reliability.DimensionSummary{Dimension: reliability.DimPredictability, Score: math.NaN()},
		Safety:         reliability.DimensionSummary{Dimension: reliability.DimSafety, Score: math.NaN()},
	}
	var buf bytes.Buffer
	require.NoError(t, renderReliability(&buf, summary, false))
	out := buf.String()
	// Stable alphabetical ordering: outcome < resource < trajectory.
	iOut := strings.Index(out, "outcome")
	iRes := strings.Index(out, "resource")
	iTraj := strings.Index(out, "trajectory")
	require.Positive(t, iOut)
	require.Positive(t, iRes)
	require.Positive(t, iTraj)
	assert.Less(t, iOut, iRes, "sub-scores must render in alphabetical order")
	assert.Less(t, iRes, iTraj)
}

// -- renderReliability: JSON mode ------------------------------------

func TestRenderReliabilityJSON_ShapeStable(t *testing.T) {
	agg := loadReliabilityAggregator(&Options{}, true)
	summary := agg.Summary(7 * 24 * time.Hour)

	var buf bytes.Buffer
	require.NoError(t, renderReliability(&buf, summary, true))

	// Assert on shape by round-tripping through a permissive map.
	var got map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &got))

	assert.Contains(t, got, "window_seconds")
	assert.Contains(t, got, "sample_count")
	assert.Contains(t, got, "overall")
	for _, dim := range []string{"consistency", "robustness", "predictability", "safety"} {
		require.Contains(t, got, dim)
		sub := got[dim].(map[string]any)
		assert.Contains(t, sub, "score")
		assert.Contains(t, sub, "sub_scores")
		assert.Contains(t, sub, "sample_count")
	}
}

func TestRenderReliabilityJSON_NaNRendersAsNull(t *testing.T) {
	// encoding/json rejects NaN outright. jsonFloat must convert
	// NaN → JSON null so the emit doesn't error.
	summary := reliability.Summary{
		Window: 7 * 24 * time.Hour, SampleCount: 0,
		Consistency:    reliability.DimensionSummary{Score: math.NaN()},
		Robustness:     reliability.DimensionSummary{Score: math.NaN()},
		Predictability: reliability.DimensionSummary{Score: math.NaN()},
		Safety:         reliability.DimensionSummary{Score: math.NaN()},
		Overall:        math.NaN(),
	}
	var buf bytes.Buffer
	require.NoError(t, renderReliability(&buf, summary, true))

	var got map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &got))
	assert.Nil(t, got["overall"], "NaN overall must be JSON null, not NaN or 0")
}

func TestJSONFloat_NaN(t *testing.T) {
	assert.Nil(t, jsonFloat(math.NaN()))
	assert.Nil(t, jsonFloat(math.Inf(1)))
	assert.Nil(t, jsonFloat(math.Inf(-1)))
	assert.Equal(t, 0.5, jsonFloat(0.5))
}

// -- helpers ---------------------------------------------------------

func TestHumanScore(t *testing.T) {
	assert.Equal(t, "  n/a", humanScore(math.NaN()))
	assert.Equal(t, "100.0%", humanScore(1.0))
	assert.Equal(t, "  0.0%", humanScore(0.0))
	assert.Equal(t, " 12.5%", humanScore(0.125))
}

func TestHumanWindow(t *testing.T) {
	assert.Equal(t, "7d", humanWindow(7*24*time.Hour))
	assert.Equal(t, "30d", humanWindow(30*24*time.Hour))
	assert.Equal(t, "1d", humanWindow(24*time.Hour))
	// Non-day-aligned durations fall through to time.Duration.String.
	assert.Equal(t, "1h30m0s", humanWindow(90*time.Minute))
}

func TestSortedKeys_Empty(t *testing.T) {
	assert.Nil(t, sortedKeys(nil))
	assert.Nil(t, sortedKeys(map[string]float64{}))
}

func TestSortedKeys_Ordering(t *testing.T) {
	got := sortedKeys(map[string]float64{"z": 1, "a": 2, "m": 3})
	assert.Equal(t, []string{"a", "m", "z"}, got)
}

func TestTernary(t *testing.T) {
	assert.Equal(t, "yes", ternary(true, "yes", "no"))
	assert.Equal(t, "no", ternary(false, "yes", "no"))
}

// -- Command wiring --------------------------------------------------

func TestNewReliabilityCmd_Registered(t *testing.T) {
	// End-to-end: NewRoot must register the reliability subcommand
	// so `rousseau reliability --help` reaches operators.
	opts := &Options{}
	root := NewRoot(opts)

	var found bool
	for _, sub := range root.Commands() {
		if sub.Name() == "reliability" {
			found = true
			break
		}
	}
	assert.True(t, found, "NewRoot must register the reliability subcommand")
}

func TestNewReliabilityCmd_HasExpectedFlags(t *testing.T) {
	cmd := newReliabilityCmd(&Options{})
	assert.NotNil(t, cmd.Flags().Lookup("window"))
	assert.NotNil(t, cmd.Flags().Lookup("json"))
	assert.NotNil(t, cmd.Flags().Lookup("synthetic"))
}
