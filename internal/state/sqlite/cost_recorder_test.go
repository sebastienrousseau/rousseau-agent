package sqlite_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
	sqlitestore "github.com/sebastienrousseau/rousseau-agent/internal/state/sqlite"
)

func TestCostRecorder_RecordAppendsRowWithEstimatedCost(t *testing.T) {
	ctx := context.Background()
	base, err := sqlitestore.Open(ctx, ":memory:")
	require.NoError(t, err)
	defer func() { _ = base.Close() }() //nolint:errcheck // best-effort test cleanup

	cs, err := sqlitestore.NewSessionCostStore(ctx, base)
	require.NoError(t, err)

	rec := sqlitestore.NewCostRecorder(cs, nil)

	// Sonnet: 1M input tokens = $3.00
	err = rec.Record(ctx, agent.CostEvent{
		SessionID: "s1",
		Provider:  "anthropic",
		Model:     "claude-sonnet-4-6",
		Usage:     agent.Usage{InputTokens: 1_000_000},
	})
	require.NoError(t, err)

	sum, err := cs.SumBySession(ctx, "s1", 0)
	require.NoError(t, err)
	assert.Equal(t, 1, sum.CompletionCount)
	assert.InDelta(t, 3.00, sum.CostUSD, 1e-9)
}

func TestCostRecorder_UnknownModelStillRecordsWithZeroCost(t *testing.T) {
	ctx := context.Background()
	base, err := sqlitestore.Open(ctx, ":memory:")
	require.NoError(t, err)
	defer func() { _ = base.Close() }() //nolint:errcheck // best-effort test cleanup

	cs, err := sqlitestore.NewSessionCostStore(ctx, base)
	require.NoError(t, err)

	rec := sqlitestore.NewCostRecorder(cs, nil)

	err = rec.Record(ctx, agent.CostEvent{
		SessionID: "s2",
		Provider:  "custom",
		Model:     "vendor-x-experimental",
		Usage:     agent.Usage{InputTokens: 500, OutputTokens: 200},
	})
	require.NoError(t, err, "record must succeed even for unpriced models")

	sum, err := cs.SumBySession(ctx, "s2", 0)
	require.NoError(t, err)
	assert.Equal(t, 1, sum.CompletionCount)
	assert.Equal(t, 0.0, sum.CostUSD, "unpriced model records with zero cost")
	// Token counts are still preserved so a later cost fill can happen.
	assert.Equal(t, 500, sum.InputTokens)
	assert.Equal(t, 200, sum.OutputTokens)
}
