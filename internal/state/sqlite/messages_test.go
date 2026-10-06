package sqlite

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/model"
)

func openV2(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "s.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() }) //nolint:errcheck // test cleanup
	return s
}

func rowCount(t *testing.T, s *Store, id string) int {
	t.Helper()
	var n int
	require.NoError(t, s.db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM session_messages WHERE session_id = ?`, id).Scan(&n))
	return n
}

func texts(sess *model.Session) []string {
	var out []string
	for _, m := range sess.Messages {
		out = append(out, m.Content[0].Text)
	}
	return out
}

// TestSave_AppendsOnlyNewMessages pins the append-only write path: a
// turn adds rows for its new messages and never rewrites stored ones.
func TestSave_AppendsOnlyNewMessages(t *testing.T) {
	s := openV2(t)
	ctx := context.Background()
	sess := model.NewSession("chat")
	sess.Append(model.NewUserText("one"))
	sess.Append(model.NewUserText("two"))
	require.NoError(t, s.Save(ctx, sess))
	require.Equal(t, 2, rowCount(t, s, sess.ID))

	// Stamp the stored first row: a rewrite would replace it.
	_, err := s.db.ExecContext(ctx, `UPDATE session_messages SET created_at = 'stamped' WHERE session_id = ? AND seq = 0`, sess.ID)
	require.NoError(t, err)

	sess.Append(model.NewUserText("three"))
	require.NoError(t, s.Save(ctx, sess))
	assert.Equal(t, 3, rowCount(t, s, sess.ID))
	var stamp string
	require.NoError(t, s.db.QueryRowContext(ctx,
		`SELECT created_at FROM session_messages WHERE session_id = ? AND seq = 0`, sess.ID).Scan(&stamp))
	assert.Equal(t, "stamped", stamp, "stored rows are never rewritten")

	require.NoError(t, s.Save(ctx, sess), "saving an unchanged view adds nothing")
	assert.Equal(t, 3, rowCount(t, s, sess.ID))

	got, err := s.Load(ctx, sess.ID)
	require.NoError(t, err)
	assert.Equal(t, []string{"one", "two", "three"}, texts(got))
	assert.Equal(t, sess.Title, got.Title)
	assert.True(t, sess.CreatedAt.Equal(got.CreatedAt))
}

// TestSave_CompressionKeepsRowsAndStoresTheSummaryAsHead pins how a
// history rewrite is stored: the compressor's summary becomes head,
// the folded rows stay stored, and later turns append as usual.
func TestSave_CompressionKeepsRowsAndStoresTheSummaryAsHead(t *testing.T) {
	s := openV2(t)
	ctx := context.Background()
	sess := model.NewSession("long")
	for _, w := range []string{"a", "b", "c", "d"} {
		sess.Append(model.NewUserText(w))
	}
	require.NoError(t, s.Save(ctx, sess))

	// What LLMCompressor does: summary + the last two messages.
	sess.Messages = append([]model.Message{model.NewUserText("[summary of a, b]")}, sess.Messages[2:]...)
	require.NoError(t, s.Save(ctx, sess))
	assert.Equal(t, 4, rowCount(t, s, sess.ID), "folded rows are kept")

	got, err := s.Load(ctx, sess.ID)
	require.NoError(t, err)
	assert.Equal(t, []string{"[summary of a, b]", "c", "d"}, texts(got))

	sess.Append(model.NewUserText("e"))
	require.NoError(t, s.Save(ctx, sess))
	assert.Equal(t, 5, rowCount(t, s, sess.ID))
	got, err = s.Load(ctx, sess.ID)
	require.NoError(t, err)
	assert.Equal(t, []string{"[summary of a, b]", "c", "d", "e"}, texts(got))

	sums, err := s.List(ctx, 0)
	require.NoError(t, err)
	assert.Equal(t, 4, sums[0].MessageCount, "message_count is the view's length")
}

// TestSave_FoldingOneMessageIsARewrite pins a compression that keeps
// the view's length and last message: it must still be stored as a
// rewrite, not mistaken for an unchanged view.
func TestSave_FoldingOneMessageIsARewrite(t *testing.T) {
	s := openV2(t)
	ctx := context.Background()
	sess := model.NewSession("short")
	sess.Append(model.NewUserText("a"))
	sess.Append(model.NewUserText("b"))
	require.NoError(t, s.Save(ctx, sess))

	sess.Messages = append([]model.Message{model.NewUserText("[summary of a]")}, sess.Messages[1:]...)
	require.NoError(t, s.Save(ctx, sess))
	got, err := s.Load(ctx, sess.ID)
	require.NoError(t, err)
	assert.Equal(t, []string{"[summary of a]", "b"}, texts(got))
}

// TestSave_UnrelatedRewriteAppendsTheNewView covers a view that shares
// nothing with the stored rows: it is appended whole and the old rows
// stay stored, out of the view.
func TestSave_UnrelatedRewriteAppendsTheNewView(t *testing.T) {
	s := openV2(t)
	ctx := context.Background()
	sess := model.NewSession("t")
	sess.Append(model.NewUserText("old"))
	require.NoError(t, s.Save(ctx, sess))

	sess.Messages = []model.Message{model.NewUserText("new 1"), model.NewUserText("new 2")}
	require.NoError(t, s.Save(ctx, sess))
	assert.Equal(t, 3, rowCount(t, s, sess.ID))
	got, err := s.Load(ctx, sess.ID)
	require.NoError(t, err)
	assert.Equal(t, []string{"new 1", "new 2"}, texts(got))

	sess.Messages = nil
	require.NoError(t, s.Save(ctx, sess))
	got, err = s.Load(ctx, sess.ID)
	require.NoError(t, err)
	assert.Empty(t, got.Messages, "an emptied view loads empty")
}

// TestSave_ConcurrentWritersLoseNoMessage pins that racing saves of one
// session serialise and every message either wrote is still stored.
func TestSave_ConcurrentWritersLoseNoMessage(t *testing.T) {
	s := openV2(t)
	ctx := context.Background()
	base := model.NewSession("race")
	base.Append(model.NewUserText("shared"))
	require.NoError(t, s.Save(ctx, base))

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, w := range []string{"from A", "from B"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			mine := *base
			mine.Messages = append([]model.Message{}, base.Messages...)
			mine.Append(model.NewUserText(w))
			errs[i] = s.Save(ctx, &mine)
		}()
	}
	wg.Wait()
	require.NoError(t, errs[0])
	require.NoError(t, errs[1])

	var bodies []string
	rows, err := s.db.QueryContext(ctx, `SELECT body FROM session_messages WHERE session_id = ?`, base.ID)
	require.NoError(t, err)
	defer rows.Close() //nolint:errcheck // test
	for rows.Next() {
		var b string
		require.NoError(t, rows.Scan(&b))
		bodies = append(bodies, b)
	}
	assert.Contains(t, bodies, "from A\n")
	assert.Contains(t, bodies, "from B\n")
}

func TestDelete_RemovesMessagesAndIndex(t *testing.T) {
	s := openV2(t)
	ctx := context.Background()
	sess := model.NewSession("gone")
	sess.Append(model.NewUserText("ephemeral marmalade"))
	require.NoError(t, s.Save(ctx, sess))
	require.NoError(t, s.Delete(ctx, sess.ID))

	assert.Zero(t, rowCount(t, s, sess.ID))
	hits, err := s.Search(ctx, "marmalade", SearchOptions{})
	require.NoError(t, err)
	assert.Empty(t, hits)
}

func TestSearch_MatchesTitlesAndReturnsOneHitPerSession(t *testing.T) {
	s := openV2(t)
	ctx := context.Background()
	sess := model.NewSession("terraform notes")
	sess.Append(model.NewUserText("terraform plan failed"))
	sess.Append(model.NewUserText("terraform apply worked"))
	require.NoError(t, s.Save(ctx, sess))

	hits, err := s.Search(ctx, "terraform", SearchOptions{})
	require.NoError(t, err)
	require.Len(t, hits, 1, "three matches in one session are one hit")

	sess.Title = "renamed to kubernetes"
	require.NoError(t, s.Save(ctx, sess))
	hits, err = s.Search(ctx, "renamed", SearchOptions{})
	require.NoError(t, err)
	require.Len(t, hits, 1, "the new title is indexed")
}

// TestEraseSender_NamespacedKeyScopesToItsTransport pins that erasing
// "signal:+44..." leaves the same number's iMessage identity alone.
func TestEraseSender_NamespacedKeyScopesToItsTransport(t *testing.T) {
	s := openV2(t)
	ctx := context.Background()
	_, err := NewIdentityStore(ctx, s)
	require.NoError(t, err)
	_, err = s.db.ExecContext(ctx, `INSERT INTO identities (id, primary_display, created_at) VALUES ('i1', 'x', 'now')`)
	require.NoError(t, err)
	for _, tp := range []string{"signal", "imessage"} {
		_, err = s.db.ExecContext(ctx,
			`INSERT INTO identity_handles (transport, sender, identity_id, verified_at) VALUES (?, '+447700900123', 'i1', 'now')`, tp)
		require.NoError(t, err)
		sess := model.NewSession(tp)
		sess.Sender = tp + ":+447700900123"
		sess.Append(model.NewUserText("hi"))
		require.NoError(t, s.Save(ctx, sess))
	}

	rep, err := s.EraseSender(ctx, "signal:+447700900123")
	require.NoError(t, err)
	assert.Len(t, rep.SessionIDs, 1)
	assert.Equal(t, int64(1), rep.Rows["identity_handles"])

	var n int
	require.NoError(t, s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM identity_handles WHERE transport = 'imessage'`).Scan(&n))
	assert.Equal(t, 1, n)
	left, err := s.ListBySender(ctx, "imessage:+447700900123", 0)
	require.NoError(t, err)
	assert.Len(t, left, 1)
}
