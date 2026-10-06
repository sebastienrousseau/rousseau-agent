// Package storetest is the contract suite every state.Store driver
// must pass. SQLite runs it against an in-memory database on every
// test run; Postgres runs it against ROUSSEAU_TEST_POSTGRES_URL (the
// Linux CI job provides one) and skips elsewhere. Parity between the
// drivers was previously maintained by hand across ~15 near-identical
// files per driver; this is the executable statement of what "same
// behaviour" means.
package storetest

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/model"
	"github.com/sebastienrousseau/rousseau-agent/internal/state"
)

// Opener returns a fresh, empty store for one subtest. The suite
// closes it; the opener registers any further cleanup with t.
type Opener func(t *testing.T) state.Store

// Run executes every contract case against stores produced by open.
func Run(t *testing.T, open Opener) {
	t.Helper()
	cases := []struct {
		name string
		fn   func(t *testing.T, s state.Store)
	}{
		{"SaveThenLoadRoundTrips", saveThenLoadRoundTrips},
		{"LoadMissingIsErrNotFound", loadMissingIsErrNotFound},
		{"SaveAppendsNewMessages", saveAppendsNewMessages},
		{"SaveReplacesDivergentHistory", saveReplacesDivergentHistory},
		{"ListIsNewestFirstAndCapped", listIsNewestFirstAndCapped},
		{"ListBySenderFiltersAndIgnoresEmpty", listBySenderFiltersAndIgnoresEmpty},
		{"DeleteRemovesAndIsIdempotent", deleteRemovesAndIsIdempotent},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := open(t)
			t.Cleanup(func() { _ = s.Close() }) //nolint:errcheck // test cleanup
			tc.fn(t, s)
		})
	}
}

func ctxFor(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// fullSession carries every content kind a session can hold so the
// round trip proves the whole wire shape, not just text.
func fullSession(title, sender string) *model.Session {
	s := model.NewSession(title)
	s.Sender = sender
	s.Append(model.NewUserText("hello"))
	s.Append(model.Message{Role: model.RoleAssistant, Content: []model.Content{
		{Kind: model.ContentText, Text: "reading"},
		{Kind: model.ContentToolUse, ToolUse: &model.ToolUse{ID: "t1", Name: "read", Input: json.RawMessage(`{"path":"/x"}`)}},
	}})
	s.Append(model.Message{Role: model.RoleUser, Content: []model.Content{
		{Kind: model.ContentToolResult, ToolResult: &model.ToolResult{ToolUseID: "t1", Output: "contents", IsError: false}},
	}})
	s.Append(model.NewUserImage("image/png", []byte{0x89, 0x50, 0x4e, 0x47}, "whatsapp"))
	s.Append(model.NewAssistantText("done"))
	return s
}

func saveThenLoadRoundTrips(t *testing.T, s state.Store) {
	ctx := ctxFor(t)
	want := fullSession("round trip", "whatsapp:+1555")
	require.NoError(t, s.Save(ctx, want))

	got, err := s.Load(ctx, want.ID)
	require.NoError(t, err)
	assert.Equal(t, want.ID, got.ID)
	assert.Equal(t, want.Title, got.Title)
	assert.Equal(t, want.Sender, got.Sender)
	require.Len(t, got.Messages, len(want.Messages))
	for i := range want.Messages {
		assert.Equal(t, want.Messages[i].Role, got.Messages[i].Role, "message %d role", i)
		assert.Equal(t, want.Messages[i].Content, got.Messages[i].Content, "message %d content", i)
		assert.WithinDuration(t, want.Messages[i].CreatedAt, got.Messages[i].CreatedAt, time.Second, "message %d timestamp", i)
	}
	assert.WithinDuration(t, want.CreatedAt, got.CreatedAt, time.Second)
	assert.WithinDuration(t, want.UpdatedAt, got.UpdatedAt, time.Second)
}

func loadMissingIsErrNotFound(t *testing.T, s state.Store) {
	_, err := s.Load(ctxFor(t), "no-such-session")
	require.ErrorIs(t, err, state.ErrNotFound)
}

func saveAppendsNewMessages(t *testing.T, s state.Store) {
	ctx := ctxFor(t)
	sess := fullSession("append", "")
	require.NoError(t, s.Save(ctx, sess))
	n := len(sess.Messages)

	sess.Append(model.NewUserText("second turn"))
	sess.Append(model.NewAssistantText("second reply"))
	require.NoError(t, s.Save(ctx, sess))

	got, err := s.Load(ctx, sess.ID)
	require.NoError(t, err)
	require.Len(t, got.Messages, n+2)
	assert.Equal(t, "second reply", got.Messages[n+1].Content[0].Text)

	// Saving an unchanged session is a no-op, not a duplicate.
	require.NoError(t, s.Save(ctx, sess))
	again, err := s.Load(ctx, sess.ID)
	require.NoError(t, err)
	assert.Len(t, again.Messages, n+2)
}

// A session whose history diverged from what was stored (compression
// rewrote it, a caller truncated it) must be replaced, not appended
// to: Load has to return exactly what was last saved.
func saveReplacesDivergentHistory(t *testing.T, s state.Store) {
	ctx := ctxFor(t)
	sess := fullSession("diverge", "")
	require.NoError(t, s.Save(ctx, sess))

	sess.Messages = []model.Message{
		model.NewUserText("summary of everything so far"),
		model.NewAssistantText("ok"),
	}
	sess.Title = "diverge (compressed)"
	require.NoError(t, s.Save(ctx, sess))

	got, err := s.Load(ctx, sess.ID)
	require.NoError(t, err)
	assert.Equal(t, "diverge (compressed)", got.Title)
	require.Len(t, got.Messages, 2)
	assert.Equal(t, "summary of everything so far", got.Messages[0].Content[0].Text)
}

func listIsNewestFirstAndCapped(t *testing.T, s state.Store) {
	ctx := ctxFor(t)
	var ids []string
	for i := 0; i < 3; i++ {
		sess := model.NewSession("list " + string(rune('a'+i)))
		sess.Append(model.NewUserText("x"))
		// Distinct UpdatedAt so the order is unambiguous.
		sess.UpdatedAt = time.Now().UTC().Add(time.Duration(i) * time.Second)
		require.NoError(t, s.Save(ctx, sess))
		ids = append(ids, sess.ID)
	}

	all, err := s.List(ctx, 0)
	require.NoError(t, err)
	require.Len(t, all, 3)
	assert.Equal(t, ids[2], all[0].ID, "newest first")
	assert.Equal(t, ids[0], all[2].ID)
	assert.Equal(t, 1, all[0].MessageCount)
	assert.NotEmpty(t, all[0].UpdatedAt)

	capped, err := s.List(ctx, 2)
	require.NoError(t, err)
	assert.Len(t, capped, 2)
	assert.Equal(t, ids[2], capped[0].ID)
}

func listBySenderFiltersAndIgnoresEmpty(t *testing.T, s state.Store) {
	ctx := ctxFor(t)
	mine := fullSession("mine", "slack:U1")
	theirs := fullSession("theirs", "slack:U2")
	legacy := fullSession("legacy", "")
	for _, sess := range []*model.Session{mine, theirs, legacy} {
		require.NoError(t, s.Save(ctx, sess))
	}

	got, err := s.ListBySender(ctx, "slack:U1", 0)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, mine.ID, got[0].ID)

	none, err := s.ListBySender(ctx, "", 0)
	require.NoError(t, err)
	assert.Empty(t, none, "an empty sender never matches legacy rows")

	capped, err := s.ListBySender(ctx, "slack:U2", 1)
	require.NoError(t, err)
	assert.Len(t, capped, 1)
}

func deleteRemovesAndIsIdempotent(t *testing.T, s state.Store) {
	ctx := ctxFor(t)
	sess := fullSession("delete me", "x")
	require.NoError(t, s.Save(ctx, sess))

	require.NoError(t, s.Delete(ctx, sess.ID))
	_, err := s.Load(ctx, sess.ID)
	require.ErrorIs(t, err, state.ErrNotFound)
	require.NoError(t, s.Delete(ctx, sess.ID), "deleting a missing session is not an error")

	all, err := s.List(ctx, 0)
	require.NoError(t, err)
	assert.Empty(t, all)
}
