package postgres

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/model"
)

// L-13: Search clamps Limit to 1..200 (zero keeps the default of 20),
// the same as the SQLite driver. Before the fix a negative limit was a
// query error here and no limit at all on SQLite.
func TestSearch_ClampsLimit(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, isolatedDSN(t))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() }) //nolint:errcheck // test cleanup
	for range 205 {
		sess := model.NewSession("ledger")
		sess.Append(model.NewUserText("invoice for the quarter"))
		require.NoError(t, s.Save(ctx, sess))
	}
	for limit, want := range map[int]int{-1: 1, 0: 20, 7: 7, 1000: 200} {
		hits, err := s.Search(ctx, "invoice", SearchOptions{Limit: limit})
		require.NoError(t, err, "limit %d", limit)
		assert.Equal(t, want, len(hits), "limit %d", limit)
	}
}
