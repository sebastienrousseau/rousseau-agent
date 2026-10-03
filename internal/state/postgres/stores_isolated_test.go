package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
	"github.com/sebastienrousseau/rousseau-agent/internal/reliability"
)

// openIsolated opens a Store in a schema of its own so these tests do
// not race the shared-table integration tests.
func openIsolated(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), isolatedDSN(t))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() }) //nolint:errcheck // test cleanup
	return s
}

// TestPGListBySender_IsScopedToTheSender pins /sessions isolation on
// Postgres: a sender sees only their own sessions, and an empty sender
// sees none rather than every legacy row.
func TestPGListBySender_IsScopedToTheSender(t *testing.T) {
	ctx := context.Background()
	s := openIsolated(t)
	for _, k := range []string{"signal:+447700900123", "imessage:+447700900123", "signal:+447700900123"} {
		sess := agent.NewSession(k)
		sess.Sender = k
		require.NoError(t, s.Save(ctx, sess))
	}
	got, err := s.ListBySender(ctx, "signal:+447700900123", 0)
	require.NoError(t, err)
	assert.Len(t, got, 2)
	got, err = s.ListBySender(ctx, "signal:+447700900123", 1)
	require.NoError(t, err)
	assert.Len(t, got, 1, "limit applies")
	got, err = s.ListBySender(ctx, "", 0)
	require.NoError(t, err)
	assert.Nil(t, got)

	hits, err := s.SearchBySender(ctx, "", "anything", SearchOptions{})
	require.NoError(t, err)
	assert.Nil(t, hits, "empty sender searches nothing")
}

func TestPGReliabilitySampleStore(t *testing.T) {
	ctx := context.Background()
	s := openIsolated(t)
	r, err := NewReliabilitySampleStore(ctx, s, nil)
	require.NoError(t, err)

	now := time.Now().UTC().Truncate(time.Millisecond)
	r.Record(reliability.Sample{At: now.Add(-2 * time.Hour), Dimension: reliability.DimSafety, SubMetric: "turn", Value: 1})
	r.Record(reliability.Sample{
		At: now, Dimension: reliability.DimSafety, SubMetric: "violation", Value: 2,
		SessionID: "sess", Bucket: "b1", Metadata: map[string]string{"severity": "low"},
	})
	r.Record(reliability.Sample{Dimension: reliability.DimConsistency, SubMetric: "resource_cv_latency", Value: 3})

	got, err := r.LoadSince(ctx, now.Add(-time.Hour))
	require.NoError(t, err)
	require.Len(t, got, 2, "only samples newer than the cutoff")
	assert.Equal(t, "violation", got[0].SubMetric)
	assert.Equal(t, "sess", got[0].SessionID)
	assert.Equal(t, "b1", got[0].Bucket)
	assert.Equal(t, "low", got[0].Metadata["severity"])
	assert.WithinDuration(t, now, got[0].At, time.Millisecond)
	assert.False(t, got[1].At.IsZero(), "a zero At is stamped on record")
	assert.Nil(t, got[1].Metadata)

	n, err := r.PruneBefore(ctx, now.Add(-time.Hour))
	require.NoError(t, err)
	assert.Equal(t, int64(1), n)
	all, err := r.LoadSince(ctx, time.Time{})
	require.NoError(t, err)
	assert.Len(t, all, 2)
}

func TestPGClaudeSessionCache_Forget(t *testing.T) {
	ctx := context.Background()
	s := openIsolated(t)
	c, err := NewClaudeSessionCache(ctx, s)
	require.NoError(t, err)
	c.Remember("abc")
	assert.True(t, c.IsKnown("abc"))
	c.Forget("abc")
	assert.False(t, c.IsKnown("abc"), "forgotten in memory and in the table")

	fresh, err := NewClaudeSessionCache(ctx, s)
	require.NoError(t, err)
	assert.False(t, fresh.IsKnown("abc"), "a new cache does not find it in the table either")
}
