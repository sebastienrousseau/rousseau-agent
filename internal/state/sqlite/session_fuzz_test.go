package sqlite

import (
	"context"
	"fmt"
	"testing"

	fuzz "github.com/google/gofuzz"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
	"github.com/sebastienrousseau/rousseau-agent/internal/state"
	"github.com/sebastienrousseau/rousseau-agent/internal/testutil/fuzztest"
)

// TestFuzz_SessionSaveLoadRoundtrip: every random Session written
// through the SQLite Store MUST come back through Load byte-for-
// byte identical on the fields the store persists.
//
// This is the load-bearing property of the session store — every
// call site trusts that "Save(s); Load(s.ID)" returns s.
func TestFuzz_SessionSaveLoadRoundtrip(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() }) //nolint:errcheck // test cleanup

	f := fuzztest.New(t).Funcs(
		// Content: force to text-only so JSON-round-trip is
		// lossless without needing bespoke populators for every
		// pointer sub-type (ToolUse.Input is a json.RawMessage
		// that must be legal JSON — easier to bypass than to
		// synthesise).
		func(c *agent.Content, ctn fuzz.Continue) {
			c.Kind = agent.ContentText
			c.Text = randPrintable(ctn, 4, 32)
		},
		// Role: constrained to legal enum values.
		func(r *agent.Role, ctn fuzz.Continue) {
			*r = []agent.Role{agent.RoleUser, agent.RoleAssistant}[ctn.Intn(2)]
		},
	)

	for i := 0; i < 200; i++ {
		var sess agent.Session
		f.Fuzz(&sess)
		// Ensure a unique, non-empty ID per iteration.
		sess.ID = fmt.Sprintf("fuzz-%d", i)

		require.NoError(t, store.Save(ctx, &sess))
		got, err := store.Load(ctx, sess.ID)
		require.NoError(t, err)

		assert.Equal(t, sess.ID, got.ID)
		assert.Equal(t, sess.Title, got.Title)
		assert.Equal(t, len(sess.Messages), len(got.Messages))
		assert.True(t, sess.CreatedAt.Equal(got.CreatedAt),
			"CreatedAt: %s vs %s", sess.CreatedAt, got.CreatedAt)
		assert.True(t, sess.UpdatedAt.Equal(got.UpdatedAt),
			"UpdatedAt: %s vs %s", sess.UpdatedAt, got.UpdatedAt)
		// Message content should survive round-trip when we
		// forced it to text-only.
		for j, want := range sess.Messages {
			require.Len(t, got.Messages[j].Content, len(want.Content))
			for k := range want.Content {
				assert.Equal(t, want.Content[k].Text, got.Messages[j].Content[k].Text)
			}
		}
	}
}

// TestFuzz_DeleteThenLoadIsErrNotFound: for every random Session
// we Save + Delete, the subsequent Load must return ErrNotFound
// (not a stale row). Property: Delete is durable.
func TestFuzz_DeleteThenLoadIsErrNotFound(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() }) //nolint:errcheck // test cleanup

	f := fuzztest.New(t)
	for i := 0; i < 100; i++ {
		var title string
		f.Fuzz(&title)
		sess := agent.NewSession(title)
		require.NoError(t, store.Save(ctx, sess))
		require.NoError(t, store.Delete(ctx, sess.ID))
		_, err := store.Load(ctx, sess.ID)
		assert.ErrorIs(t, err, state.ErrNotFound)
	}
}

// TestFuzz_UpsertPreservesLastWrite: two consecutive Saves of the
// same ID leave the store with the LAST write's contents.
// Property: ON CONFLICT DO UPDATE truly upserts.
func TestFuzz_UpsertPreservesLastWrite(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() }) //nolint:errcheck // test cleanup

	f := fuzztest.New(t)
	for i := 0; i < 100; i++ {
		var v1Title, v2Title string
		f.Fuzz(&v1Title)
		f.Fuzz(&v2Title)

		sess := agent.NewSession(v1Title)
		require.NoError(t, store.Save(ctx, sess))
		sess.Title = v2Title
		sess.Append(agent.NewUserText("second"))
		require.NoError(t, store.Save(ctx, sess))

		got, err := store.Load(ctx, sess.ID)
		require.NoError(t, err)
		assert.Equal(t, v2Title, got.Title, "last write must win")
		assert.Len(t, got.Messages, 1)
	}
}

// randPrintable pulls a printable-ASCII string of length in
// [minLen, maxLen]. Duplicates the fuzztest helper's private
// function so the Content populator can call it inline without
// widening fuzztest's public surface.
func randPrintable(c fuzz.Continue, minLen, maxLen int) string {
	n := minLen
	if maxLen > minLen {
		n += c.Intn(maxLen - minLen + 1)
	}
	const printable = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789 !\"#$%&'()*+,-./:;<=>?@[\\]^_`{|}~"
	b := make([]byte, n)
	for i := range b {
		b[i] = printable[c.Intn(len(printable))]
	}
	return string(b)
}
