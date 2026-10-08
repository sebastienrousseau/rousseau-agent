package sqlite

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/model"
)

func TestRecallSearcher_Roundtrip(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, ":memory:")
	require.NoError(t, err)
	defer func() { _ = s.Close() }() //nolint:errcheck // test cleanup

	sess := model.NewSession("previous kubernetes chat")
	sess.Append(model.NewUserText("we discussed pod affinity and helm charts"))
	require.NoError(t, s.Save(ctx, sess))

	r := NewRecallSearcher(s)
	hits, err := r.Search(ctx, "", "kubernetes", 5)
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
	_, err = r.Search(ctx, "", "", 5)
	assert.Error(t, err)
}

// Scoped recall never returns another sender's session; the empty
// sender (local chat) is a scope of its own.
func TestRecallSearcher_ScopedToSender(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, ":memory:")
	require.NoError(t, err)
	defer func() { _ = s.Close() }() //nolint:errcheck // test cleanup
	save := func(sender, text string) string {
		sess := model.NewSession("chat")
		sess.Sender = sender
		sess.Append(model.NewUserText(text))
		require.NoError(t, s.Save(ctx, sess))
		return sess.ID
	}
	alice := save("telegram:alice", "invoice salary details for alice")
	bob := save("telegram:bob", "invoice question from bob")
	local := save("", "invoice notes from the local operator")

	r := NewRecallSearcher(s)
	for sender, want := range map[string]string{"telegram:bob": bob, "telegram:alice": alice, "": local} {
		hits, err := r.Search(ctx, sender, "invoice", 10)
		require.NoError(t, err)
		require.Len(t, hits, 1, "sender %q", sender)
		assert.Equal(t, want, hits[0].SessionID)
	}
	hits, err := r.Search(ctx, "telegram:nobody", "invoice", 10)
	require.NoError(t, err)
	assert.Empty(t, hits)
}
