package sqlite_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
	sqlitestore "github.com/sebastienrousseau/rousseau-agent/internal/state/sqlite"
)

// openStore is a thin test helper — sqlite in-memory + the session-cost store on top.
func openCostStore(t *testing.T) (*sqlitestore.SessionCostStore, context.Context) {
	t.Helper()
	ctx := context.Background()
	base, err := sqlitestore.Open(ctx, ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = base.Close() }) //nolint:errcheck // best-effort test cleanup
	cs, err := sqlitestore.NewSessionCostStore(ctx, base)
	require.NoError(t, err)
	return cs, ctx
}

func TestSessionCostStore_RecordAndSum(t *testing.T) {
	cs, ctx := openCostStore(t)

	require.NoError(t, cs.Record(ctx, sqlitestore.CostRecord{
		SessionID: "s1",
		Provider:  "anthropic",
		Model:     "claude-sonnet-4-6",
		Usage: agent.Usage{
			InputTokens:              1000,
			OutputTokens:             500,
			CacheReadInputTokens:     2000,
			CacheCreationInputTokens: 300,
		},
		CostUSD: 0.01,
	}))
	require.NoError(t, cs.Record(ctx, sqlitestore.CostRecord{
		SessionID: "s1",
		Provider:  "anthropic",
		Model:     "claude-sonnet-4-6",
		Usage:     agent.Usage{InputTokens: 500, OutputTokens: 200},
		CostUSD:   0.005,
	}))

	sum, err := cs.SumBySession(ctx, "s1", 0)
	require.NoError(t, err)
	assert.Equal(t, 2, sum.CompletionCount)
	assert.Equal(t, 1500, sum.InputTokens)
	assert.Equal(t, 700, sum.OutputTokens)
	assert.Equal(t, 2000, sum.CacheReadTokens)
	assert.Equal(t, 300, sum.CacheCreationTokens)
	assert.InDelta(t, 0.015, sum.CostUSD, 1e-9)
}

func TestSessionCostStore_SumForUnknownSessionIsZero(t *testing.T) {
	cs, ctx := openCostStore(t)
	sum, err := cs.SumBySession(ctx, "does-not-exist", 0)
	require.NoError(t, err)
	assert.Equal(t, 0, sum.CompletionCount)
	assert.Equal(t, 0.0, sum.CostUSD)
}

func TestSessionCostStore_TopSessionsOrdersDescByCost(t *testing.T) {
	cs, ctx := openCostStore(t)

	for _, r := range []sqlitestore.CostRecord{
		{SessionID: "cheap", Provider: "p", Model: "m", CostUSD: 0.01},
		{SessionID: "mid", Provider: "p", Model: "m", CostUSD: 0.20},
		{SessionID: "spendy", Provider: "p", Model: "m", CostUSD: 1.50},
		{SessionID: "zero", Provider: "p", Model: "m", CostUSD: 0}, // excluded from top
	} {
		require.NoError(t, cs.Record(ctx, r))
	}

	top, err := cs.TopSessions(ctx, 0, 10)
	require.NoError(t, err)
	require.Len(t, top, 3, "zero-cost sessions must be excluded")
	assert.Equal(t, "spendy", top[0].SessionID)
	assert.Equal(t, "mid", top[1].SessionID)
	assert.Equal(t, "cheap", top[2].SessionID)
}

func TestSessionCostStore_SinceWindowExcludesOldRecords(t *testing.T) {
	cs, ctx := openCostStore(t)

	old := time.Now().Add(-48 * time.Hour)
	require.NoError(t, cs.Record(ctx, sqlitestore.CostRecord{
		SessionID: "s1", At: old, Provider: "p", Model: "m", CostUSD: 5.00,
	}))
	require.NoError(t, cs.Record(ctx, sqlitestore.CostRecord{
		SessionID: "s1", Provider: "p", Model: "m", CostUSD: 1.00,
	}))

	all, err := cs.SumBySession(ctx, "s1", 0)
	require.NoError(t, err)
	assert.InDelta(t, 6.00, all.CostUSD, 1e-9)

	last24h, err := cs.SumBySession(ctx, "s1", 24*time.Hour)
	require.NoError(t, err)
	assert.InDelta(t, 1.00, last24h.CostUSD, 1e-9)
}

func TestNewSessionCostStore_RejectsNilStore(t *testing.T) {
	_, err := sqlitestore.NewSessionCostStore(context.Background(), nil)
	assert.Error(t, err)
}
