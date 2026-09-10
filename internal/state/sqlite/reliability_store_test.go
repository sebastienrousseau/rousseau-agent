package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/reliability"
)

// Regression tests for the SQLite reliability_samples backing
// store. The store is the durable half of the reliability system —
// the in-memory Aggregator loses everything on restart, so ordinary
// operators reading `rousseau reliability` from a shell process
// depend on this table containing every sample the daemon has
// emitted since the last prune.

func openReliabilityStore(t *testing.T) *ReliabilitySampleStore {
	t.Helper()
	s, err := Open(context.Background(), ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() }) //nolint:errcheck // test cleanup
	r, err := NewReliabilitySampleStore(context.Background(), s, nil)
	require.NoError(t, err)
	return r
}

func TestReliabilitySampleStore_RoundTrip(t *testing.T) {
	r := openReliabilityStore(t)
	at := time.Now().UTC().Truncate(time.Millisecond)
	r.Record(reliability.Sample{
		At: at, Dimension: reliability.DimSafety, SubMetric: "turn",
		Value: 1, SessionID: "sess", Bucket: "b1",
		Metadata: map[string]string{"severity": "low"},
	})

	got, err := r.LoadSince(context.Background(), at.Add(-time.Minute))
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, reliability.DimSafety, got[0].Dimension)
	assert.Equal(t, "turn", got[0].SubMetric)
	assert.Equal(t, 1.0, got[0].Value)
	assert.Equal(t, "sess", got[0].SessionID)
	assert.Equal(t, "b1", got[0].Bucket)
	assert.Equal(t, "low", got[0].Metadata["severity"])
	// Timestamp round-trip: at.Truncate(ms) preserved through
	// RFC3339Nano.
	assert.WithinDuration(t, at, got[0].At, time.Millisecond)
}

func TestReliabilitySampleStore_LoadSinceFiltersByCutoff(t *testing.T) {
	r := openReliabilityStore(t)
	now := time.Now().UTC()
	r.Record(reliability.Sample{At: now.Add(-2 * time.Hour), Dimension: reliability.DimSafety, SubMetric: "turn", Value: 1})
	r.Record(reliability.Sample{At: now.Add(-30 * time.Minute), Dimension: reliability.DimSafety, SubMetric: "turn", Value: 1})

	got, err := r.LoadSince(context.Background(), now.Add(-1*time.Hour))
	require.NoError(t, err)
	assert.Len(t, got, 1, "only the sample newer than the cutoff should return")
}

func TestReliabilitySampleStore_LoadSinceOrderIsInsertion(t *testing.T) {
	r := openReliabilityStore(t)
	// Same timestamp but distinct sub-metrics — ORDER BY rowid
	// preserves insertion order, so the assertion is stable
	// regardless of how the DB stores equal timestamps.
	at := time.Now().UTC()
	for i, sub := range []string{"turn", "violation", "resource_cv_latency"} {
		r.Record(reliability.Sample{
			At: at, Dimension: reliability.DimSafety, SubMetric: sub, Value: float64(i),
		})
	}

	got, err := r.LoadSince(context.Background(), at.Add(-time.Minute))
	require.NoError(t, err)
	require.Len(t, got, 3)
	assert.Equal(t, "turn", got[0].SubMetric)
	assert.Equal(t, "violation", got[1].SubMetric)
	assert.Equal(t, "resource_cv_latency", got[2].SubMetric)
}

func TestReliabilitySampleStore_MetadataOmittedRoundTrips(t *testing.T) {
	// A sample with nil Metadata must survive the round-trip
	// without a JSON-decode error.
	r := openReliabilityStore(t)
	r.Record(reliability.Sample{
		At: time.Now().UTC(), Dimension: reliability.DimConsistency,
		SubMetric: "resource_cv_latency", Value: 42,
	})

	got, err := r.LoadSince(context.Background(), time.Time{})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Nil(t, got[0].Metadata)
}

func TestReliabilitySampleStore_ZeroAtStamped(t *testing.T) {
	// A caller that passes a zero At (never happens in production;
	// Aggregator stamps first) must have the store stamp it —
	// otherwise the row would fail the NOT NULL constraint.
	r := openReliabilityStore(t)
	before := time.Now().UTC()
	r.Record(reliability.Sample{Dimension: reliability.DimSafety, SubMetric: "turn", Value: 1})
	after := time.Now().UTC()

	got, err := r.LoadSince(context.Background(), time.Time{})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.True(t, !got[0].At.Before(before) && !got[0].At.After(after),
		"zero At must be stamped to now(), landing between the two markers")
}

func TestReliabilitySampleStore_PruneBefore(t *testing.T) {
	r := openReliabilityStore(t)
	now := time.Now().UTC()
	r.Record(reliability.Sample{At: now.Add(-40 * 24 * time.Hour), Dimension: reliability.DimSafety, SubMetric: "turn", Value: 1})
	r.Record(reliability.Sample{At: now.Add(-1 * 24 * time.Hour), Dimension: reliability.DimSafety, SubMetric: "turn", Value: 1})

	n, err := r.PruneBefore(context.Background(), now.Add(-30*24*time.Hour))
	require.NoError(t, err)
	assert.Equal(t, int64(1), n, "one sample beyond the 30d cutoff must be pruned")

	// The kept sample still queryable.
	got, err := r.LoadSince(context.Background(), time.Time{})
	require.NoError(t, err)
	assert.Len(t, got, 1)
}

func TestReliabilitySampleStore_NilStoreSafe(t *testing.T) {
	// Record must not panic on a nil store — defensive so the
	// daemon assembly's nil-Recorder default keeps working.
	var r *ReliabilitySampleStore
	assert.NotPanics(t, func() {
		r.Record(reliability.Sample{Dimension: reliability.DimSafety, SubMetric: "turn", Value: 1})
	})
}

func TestReliabilitySampleStore_LoadSinceOnClosedDBErrors(t *testing.T) {
	s, err := Open(context.Background(), ":memory:")
	require.NoError(t, err)
	r, err := NewReliabilitySampleStore(context.Background(), s, nil)
	require.NoError(t, err)
	_ = s.Close() //nolint:errcheck // deliberate close before query

	_, err = r.LoadSince(context.Background(), time.Time{})
	assert.Error(t, err, "querying a closed DB must surface the error rather than return nil samples")
}
