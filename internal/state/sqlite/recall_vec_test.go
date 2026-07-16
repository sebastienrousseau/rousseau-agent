package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func openRecallVectors(t *testing.T) *RecallVectors {
	t.Helper()
	s, err := Open(context.Background(), ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.db.Close() }) //nolint:errcheck // test cleanup
	rv, err := NewRecallVectors(context.Background(), s)
	require.NoError(t, err)
	return rv
}

func mkRow(session string, msg int64, chunk int, at time.Time) VectorRow {
	return VectorRow{
		SessionID: session, MessageID: msg, ChunkIndex: chunk,
		Role: "user", Text: "hello world",
		Embedding: []byte{0x00, 0x00, 0x80, 0x3F, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}, // [1,0,0]
		CreatedAt: at, Embedder: "test",
	}
}

func TestRecallVectors_PutAndSince(t *testing.T) {
	rv := openRecallVectors(t)
	now := time.Now().UTC()
	require.NoError(t, rv.Put(context.Background(), mkRow("s1", 1, 0, now)))
	require.NoError(t, rv.Put(context.Background(), mkRow("s1", 1, 1, now.Add(time.Second))))

	rows, err := rv.Since(context.Background(), now.Add(-time.Minute))
	require.NoError(t, err)
	assert.Len(t, rows, 2)
}

func TestRecallVectors_PutReplacesOnConflict(t *testing.T) {
	rv := openRecallVectors(t)
	now := time.Now().UTC()
	require.NoError(t, rv.Put(context.Background(), mkRow("s1", 1, 0, now)))
	// Same natural key with different text — should replace.
	r := mkRow("s1", 1, 0, now)
	r.Text = "replaced text"
	require.NoError(t, rv.Put(context.Background(), r))

	count, err := rv.Count(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, count)

	rows, err := rv.All(context.Background())
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "replaced text", rows[0].Text)
}

func TestRecallVectors_SinceHonoursWindow(t *testing.T) {
	rv := openRecallVectors(t)
	now := time.Now().UTC()
	old := now.Add(-24 * time.Hour)
	require.NoError(t, rv.Put(context.Background(), mkRow("s1", 1, 0, old)))
	require.NoError(t, rv.Put(context.Background(), mkRow("s1", 2, 0, now)))

	rows, err := rv.Since(context.Background(), now.Add(-time.Hour))
	require.NoError(t, err)
	assert.Len(t, rows, 1)
	assert.EqualValues(t, 2, rows[0].MessageID)
}

func TestRecallVectors_PurgeOlderThan(t *testing.T) {
	rv := openRecallVectors(t)
	now := time.Now().UTC()
	require.NoError(t, rv.Put(context.Background(), mkRow("s1", 1, 0, now.Add(-48*time.Hour))))
	require.NoError(t, rv.Put(context.Background(), mkRow("s1", 2, 0, now)))

	deleted, err := rv.PurgeOlderThan(context.Background(), now.Add(-24*time.Hour))
	require.NoError(t, err)
	assert.EqualValues(t, 1, deleted)

	count, err := rv.Count(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, count)
}

func TestRecallVectors_CountEmpty(t *testing.T) {
	rv := openRecallVectors(t)
	n, err := rv.Count(context.Background())
	require.NoError(t, err)
	assert.Zero(t, n)
}
