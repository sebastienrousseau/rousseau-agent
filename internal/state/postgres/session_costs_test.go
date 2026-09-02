package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
)

// openCostStore opens a SessionCostStore against the CI/dev
// Postgres and truncates the session_costs table so each test
// starts clean. Guarded on ROUSSEAU_TEST_POSTGRES_URL like the
// other integration tests.
func openCostStore(t *testing.T) (*SessionCostStore, context.Context) {
	t.Helper()
	ctx := context.Background()
	store, err := Open(ctx, requirePG(t))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() }) //nolint:errcheck // test cleanup

	cs, err := NewSessionCostStore(ctx, store)
	require.NoError(t, err)
	_, err = store.db.ExecContext(ctx, `TRUNCATE TABLE session_costs`)
	require.NoError(t, err)
	return cs, ctx
}

// -- unit-only (no DB) --

func TestNewSessionCostStore_SchemaIdempotent(t *testing.T) {
	// Compile-check the const we ship — an accidental rename would
	// cause the daemon to fail schema-apply on upgrade.
	assert.Contains(t, sessionCostsSchema, "CREATE TABLE IF NOT EXISTS session_costs")
	assert.Contains(t, sessionCostsSchema, "TIMESTAMPTZ NOT NULL DEFAULT NOW()")
	assert.Contains(t, sessionCostsSchema, "cost_usd       DOUBLE PRECISION NOT NULL DEFAULT 0")
	assert.Contains(t, sessionCostsSchema, "input_tokens   BIGINT NOT NULL DEFAULT 0")
	assert.Contains(t, sessionCostsSchema, "idx_session_costs_session_id_at")
	assert.Contains(t, sessionCostsSchema, "idx_session_costs_at")
}

func TestNewSessionCostStore_RejectsNilStore(t *testing.T) {
	// Matches SQLite driver's nil-check contract so caller
	// assertions do not need per-driver branches.
	_, err := NewSessionCostStore(context.Background(), nil)
	assert.Error(t, err)
}

// -- integration --

func TestIntegration_SessionCosts_RecordAndSum(t *testing.T) {
	cs, ctx := openCostStore(t)

	require.NoError(t, cs.Record(ctx, CostRecord{
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
	require.NoError(t, cs.Record(ctx, CostRecord{
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

func TestIntegration_SessionCosts_SumForUnknownSessionIsZero(t *testing.T) {
	// Missing session returns zero-value + nil, not ErrNoRows —
	// matches SQLite so callers do not need per-driver branches.
	cs, ctx := openCostStore(t)
	sum, err := cs.SumBySession(ctx, "does-not-exist", 0)
	require.NoError(t, err)
	assert.Equal(t, 0, sum.CompletionCount)
	assert.Equal(t, 0.0, sum.CostUSD)
}

func TestIntegration_SessionCosts_TopSessionsOrdersDescByCost(t *testing.T) {
	cs, ctx := openCostStore(t)

	for _, r := range []CostRecord{
		{SessionID: "cheap", Provider: "p", Model: "m", CostUSD: 0.01},
		{SessionID: "mid", Provider: "p", Model: "m", CostUSD: 0.20},
		{SessionID: "spendy", Provider: "p", Model: "m", CostUSD: 1.50},
		{SessionID: "zero", Provider: "p", Model: "m", CostUSD: 0}, // excluded
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

func TestIntegration_SessionCosts_SinceWindowExcludesOldRecords(t *testing.T) {
	// The since-window path is the driver's most subtle change: the
	// SQLite version compares TEXT-formatted timestamps, Postgres
	// compares TIMESTAMPTZ. This proves both return identical
	// numbers over the same fixture.
	cs, ctx := openCostStore(t)

	old := time.Now().Add(-48 * time.Hour)
	require.NoError(t, cs.Record(ctx, CostRecord{
		SessionID: "s1", At: old, Provider: "p", Model: "m", CostUSD: 5.00,
	}))
	require.NoError(t, cs.Record(ctx, CostRecord{
		SessionID: "s1", Provider: "p", Model: "m", CostUSD: 1.00,
	}))

	all, err := cs.SumBySession(ctx, "s1", 0)
	require.NoError(t, err)
	assert.InDelta(t, 6.00, all.CostUSD, 1e-9)

	last24h, err := cs.SumBySession(ctx, "s1", 24*time.Hour)
	require.NoError(t, err)
	assert.InDelta(t, 1.00, last24h.CostUSD, 1e-9)
}

func TestIntegration_SessionCosts_TopSessionsRespectsSinceWindow(t *testing.T) {
	// TopSessions with a since window must also skip old rows.
	// Uses a session ("legacy") that only has old rows and would
	// otherwise place first by lifetime cost.
	cs, ctx := openCostStore(t)

	old := time.Now().Add(-48 * time.Hour)
	require.NoError(t, cs.Record(ctx, CostRecord{
		SessionID: "legacy", At: old, Provider: "p", Model: "m", CostUSD: 10.00,
	}))
	require.NoError(t, cs.Record(ctx, CostRecord{
		SessionID: "recent", Provider: "p", Model: "m", CostUSD: 1.00,
	}))

	top, err := cs.TopSessions(ctx, 24*time.Hour, 10)
	require.NoError(t, err)
	require.Len(t, top, 1, "old rows must be excluded from since-windowed top")
	assert.Equal(t, "recent", top[0].SessionID)
}
