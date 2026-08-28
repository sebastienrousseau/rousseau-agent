package sqlite

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
)

func TestRecallSearcher_Roundtrip(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, ":memory:")
	require.NoError(t, err)
	defer func() { _ = s.Close() }() //nolint:errcheck // test cleanup

	sess := agent.NewSession("previous kubernetes chat")
	sess.Append(agent.NewUserText("we discussed pod affinity and helm charts"))
	require.NoError(t, s.Save(ctx, sess))

	r := NewRecallSearcher(s)
	hits, err := r.Search(ctx, "kubernetes", 5)
	require.NoError(t, err)
	require.NotEmpty(t, hits)
	assert.Equal(t, sess.ID, hits[0].SessionID)
}

func TestRecallSearcher_ErrorPropagates(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, ":memory:")
	require.NoError(t, err)
	defer func() { _ = s.Close() }() //nolint:errcheck // test cleanup
	r := NewRecallSearcher(s)
	// Empty query surfaces the underlying Search error.
	_, err = r.Search(ctx, "", 5)
	assert.Error(t, err)
}
