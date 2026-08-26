package letta_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/memory/letta"
)

// TestCoreMemory_ByteSizeEmptyIsZero proves an empty core block costs
// nothing against the budget (rather than the 2 bytes of "[]").
func TestCoreMemory_ByteSizeEmptyIsZero(t *testing.T) {
	assert.Zero(t, letta.CoreMemory{SessionID: "s"}.ByteSize())
	assert.Positive(t, letta.CoreMemory{
		SessionID: "s",
		Facts:     []letta.Fact{{Key: "k", Value: "v"}},
	}.ByteSize())
}

func TestAppendArchival_RequiresSessionID(t *testing.T) {
	s := letta.NewMemoryStore()
	err := s.AppendArchival(context.Background(), letta.ArchivalEntry{Text: "orphan"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "SessionID is required")
}

func TestDemoteOldest_NonPositiveCountIsNoop(t *testing.T) {
	ctx := context.Background()
	s := letta.NewMemoryStore()
	require.NoError(t, s.WriteCore(ctx, letta.CoreMemory{
		SessionID: "s1",
		Facts: []letta.Fact{
			{Key: "a", Value: "1", CreatedAt: time.Unix(1, 0)},
			{Key: "b", Value: "2", CreatedAt: time.Unix(2, 0)},
		},
	}))

	for _, n := range []int{0, -3} {
		require.NoError(t, s.DemoteOldest(ctx, "s1", n))
	}
	m, err := s.LoadCore(ctx, "s1")
	require.NoError(t, err)
	assert.Equal(t, []string{"a", "b"}, factKeys(m.Facts))
}

// TestDemoteOldest_MoreThanStoredDrainsCore proves an over-long demote
// empties core rather than slicing out of range.
func TestDemoteOldest_MoreThanStoredDrainsCore(t *testing.T) {
	ctx := context.Background()
	s := letta.NewMemoryStore()
	require.NoError(t, s.WriteCore(ctx, letta.CoreMemory{
		SessionID: "s1",
		Facts: []letta.Fact{
			{Key: "alpha", Value: "one", CreatedAt: time.Unix(1, 0)},
			{Key: "beta", Value: "two", CreatedAt: time.Unix(2, 0)},
		},
	}))

	require.NoError(t, s.DemoteOldest(ctx, "s1", 99))

	m, err := s.LoadCore(ctx, "s1")
	require.NoError(t, err)
	assert.Empty(t, m.Facts)

	hits, err := s.SearchArchival(ctx, "s1", "alpha beta", 0)
	require.NoError(t, err)
	assert.Len(t, hits, 2, "both facts land in archival")
}

// TestSearchArchival_LimitDefaultsAndCaps covers both the "limit <= 0
// means 10" default and the explicit truncation of an over-long hit list.
func TestSearchArchival_LimitDefaultsAndCaps(t *testing.T) {
	ctx := context.Background()
	s := letta.NewMemoryStore()
	for i := range 12 {
		require.NoError(t, s.AppendArchival(ctx, letta.ArchivalEntry{
			SessionID: "s1",
			Text:      "widget note",
			CreatedAt: time.Unix(int64(i+1), 0),
		}))
	}

	defaulted, err := s.SearchArchival(ctx, "s1", "widget", 0)
	require.NoError(t, err)
	assert.Len(t, defaulted, 10, "a non-positive limit falls back to 10")

	capped, err := s.SearchArchival(ctx, "s1", "widget", 3)
	require.NoError(t, err)
	assert.Len(t, capped, 3)
}

// TestSearchArchival_TiesBreakByRecency proves equally-scored entries
// come back newest-first.
func TestSearchArchival_TiesBreakByRecency(t *testing.T) {
	ctx := context.Background()
	s := letta.NewMemoryStore()
	require.NoError(t, s.AppendArchival(ctx, letta.ArchivalEntry{
		SessionID: "s1", Text: "widget older", CreatedAt: time.Unix(100, 0),
	}))
	require.NoError(t, s.AppendArchival(ctx, letta.ArchivalEntry{
		SessionID: "s1", Text: "widget newer", CreatedAt: time.Unix(200, 0),
	}))

	hits, err := s.SearchArchival(ctx, "s1", "widget", 10)
	require.NoError(t, err)
	require.Len(t, hits, 2)
	assert.Equal(t, "widget newer", hits[0].Text)
	assert.Equal(t, "widget older", hits[1].Text)
}

// TestSearchArchival_SingleCharTokensAreIgnored proves a stray one-letter
// word in the query cannot match everything in the store.
func TestSearchArchival_SingleCharTokensAreIgnored(t *testing.T) {
	ctx := context.Background()
	s := letta.NewMemoryStore()
	require.NoError(t, s.AppendArchival(ctx, letta.ArchivalEntry{
		SessionID: "s1", Text: "a rusty anchor", CreatedAt: time.Unix(1, 0),
	}))

	none, err := s.SearchArchival(ctx, "s1", "a", 10)
	require.NoError(t, err)
	assert.Empty(t, none, "a one-character token scores nothing")

	some, err := s.SearchArchival(ctx, "s1", "a anchor", 10)
	require.NoError(t, err)
	assert.Len(t, some, 1, "the real token still matches")
}
