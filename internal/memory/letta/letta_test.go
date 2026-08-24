package letta_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/memory/letta"
)

func TestNewSQLiteStore_StillScaffold(t *testing.T) {
	_, err := letta.NewSQLiteStore()
	assert.ErrorIs(t, err, letta.ErrScaffold)
}

func TestMemoryStore_CoreRoundtrip(t *testing.T) {
	s := letta.NewMemoryStore()
	ctx := context.Background()

	// Unknown session returns a zero CoreMemory with the default budget.
	m, err := s.LoadCore(ctx, "sess-1")
	require.NoError(t, err)
	assert.Equal(t, "sess-1", m.SessionID)
	assert.Equal(t, letta.DefaultCoreBytes, m.MaxBytes)
	assert.Empty(t, m.Facts)

	m.Facts = []letta.Fact{{Key: "name", Value: "Sebastian", CreatedAt: time.Now().UTC()}}
	require.NoError(t, s.WriteCore(ctx, m))

	got, err := s.LoadCore(ctx, "sess-1")
	require.NoError(t, err)
	require.Len(t, got.Facts, 1)
	assert.Equal(t, "Sebastian", got.Facts[0].Value)
	assert.False(t, got.UpdatedAt.IsZero())
}

func TestMemoryStore_WriteCoreDemotesWhenBudgetExceeded(t *testing.T) {
	s := letta.NewMemoryStore()
	ctx := context.Background()

	// Each JSON-encoded fact runs ~120-160B once the timestamps are
	// included — pick a budget that fits ~2 of them so the oldest
	// two get demoted and the newest two survive.
	now := time.Now().UTC()
	facts := []letta.Fact{
		{Key: "a", Value: strings.Repeat("A", 30), CreatedAt: now.Add(-4 * time.Minute)},
		{Key: "b", Value: strings.Repeat("B", 30), CreatedAt: now.Add(-3 * time.Minute)},
		{Key: "c", Value: strings.Repeat("C", 30), CreatedAt: now.Add(-2 * time.Minute)},
		{Key: "d", Value: strings.Repeat("D", 30), CreatedAt: now.Add(-1 * time.Minute)},
	}
	require.NoError(t, s.WriteCore(ctx, letta.CoreMemory{
		SessionID: "sess-2",
		Facts:     facts,
		MaxBytes:  350,
	}))

	got, err := s.LoadCore(ctx, "sess-2")
	require.NoError(t, err)
	assert.LessOrEqual(t, got.ByteSize(), 350, "core memory must respect MaxBytes")
	assert.NotEmpty(t, got.Facts, "should retain some facts")
	// The most recent facts survive.
	remaining := factKeys(got.Facts)
	assert.Contains(t, remaining, "d", "most recent fact must remain")
	assert.NotContains(t, remaining, "a", "oldest fact must have been demoted")

	// Demoted facts land in archival.
	hits, err := s.SearchArchival(ctx, "sess-2", "AA", 10)
	require.NoError(t, err)
	assert.NotEmpty(t, hits, "demoted fact must be searchable in archival")
}

func TestMemoryStore_DemoteOldest_ExplicitCall(t *testing.T) {
	s := letta.NewMemoryStore()
	ctx := context.Background()
	now := time.Now().UTC()
	require.NoError(t, s.WriteCore(ctx, letta.CoreMemory{
		SessionID: "sess-3",
		Facts: []letta.Fact{
			{Key: "old", Value: "past", CreatedAt: now.Add(-time.Hour)},
			{Key: "new", Value: "future", CreatedAt: now},
		},
	}))
	require.NoError(t, s.DemoteOldest(ctx, "sess-3", 1))

	got, err := s.LoadCore(ctx, "sess-3")
	require.NoError(t, err)
	require.Len(t, got.Facts, 1)
	assert.Equal(t, "new", got.Facts[0].Key)

	hits, err := s.SearchArchival(ctx, "sess-3", "past", 10)
	require.NoError(t, err)
	require.Len(t, hits, 1)
	assert.Contains(t, hits[0].Text, "past")
}

func TestMemoryStore_DemoteOldest_UnknownSessionNoop(t *testing.T) {
	s := letta.NewMemoryStore()
	require.NoError(t, s.DemoteOldest(context.Background(), "nope", 5))
}

func TestMemoryStore_SearchArchivalRanksBySubstringHits(t *testing.T) {
	s := letta.NewMemoryStore()
	ctx := context.Background()

	entries := []letta.ArchivalEntry{
		{SessionID: "sess-4", Text: "user loves rust and hates cmake"},
		{SessionID: "sess-4", Text: "user picked cmake for the ffi glue"},
		{SessionID: "sess-4", Text: "unrelated fact about coffee"},
	}
	for _, e := range entries {
		require.NoError(t, s.AppendArchival(ctx, e))
	}

	hits, err := s.SearchArchival(ctx, "sess-4", "cmake rust", 10)
	require.NoError(t, err)
	require.Len(t, hits, 2, "coffee entry must not rank")
	assert.Contains(t, hits[0].Text, "rust", "rust+cmake match ranks above cmake-only")
}

func TestMemoryStore_SearchEmptyQueryReturnsNothing(t *testing.T) {
	s := letta.NewMemoryStore()
	hits, err := s.SearchArchival(context.Background(), "sess", " ", 10)
	require.NoError(t, err)
	assert.Empty(t, hits)
}

func TestMemoryStore_AppendArchivalStampsCreatedAt(t *testing.T) {
	s := letta.NewMemoryStore()
	ctx := context.Background()
	require.NoError(t, s.AppendArchival(ctx, letta.ArchivalEntry{SessionID: "s", Text: "hello world"}))
	hits, err := s.SearchArchival(ctx, "s", "hello", 1)
	require.NoError(t, err)
	require.Len(t, hits, 1)
	assert.False(t, hits[0].CreatedAt.IsZero())
}

func TestMemoryStore_WriteCore_RequiresSessionID(t *testing.T) {
	s := letta.NewMemoryStore()
	err := s.WriteCore(context.Background(), letta.CoreMemory{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "SessionID")
}

func TestMemoryStore_ReturnedCoreIsCopy(t *testing.T) {
	s := letta.NewMemoryStore()
	ctx := context.Background()
	require.NoError(t, s.WriteCore(ctx, letta.CoreMemory{
		SessionID: "s", Facts: []letta.Fact{{Key: "k", Value: "v", CreatedAt: time.Now().UTC()}},
	}))
	got, err := s.LoadCore(ctx, "s")
	require.NoError(t, err)
	got.Facts[0].Value = "mutated"

	// Second load should still show "v".
	fresh, err := s.LoadCore(ctx, "s")
	require.NoError(t, err)
	assert.Equal(t, "v", fresh.Facts[0].Value)
}

// factKeys is a tiny helper for tests that inspect which facts survived.
func factKeys(fs []letta.Fact) []string {
	out := make([]string, len(fs))
	for i, f := range fs {
		out[i] = f.Key
	}
	return out
}
