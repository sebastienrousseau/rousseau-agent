package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
)

// openSearchStore opens a Store, ensures the base schema + the
// search schema, and truncates sessions so each test starts clean.
// Guarded on ROUSSEAU_TEST_POSTGRES_URL like the other integration
// tests in this package.
func openSearchStore(t *testing.T) (*Store, context.Context) {
	t.Helper()
	ctx := context.Background()
	store, err := Open(ctx, requirePG(t))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() }) //nolint:errcheck // test cleanup

	require.NoError(t, store.EnsureSearch(ctx))
	// TRUNCATE proves the schema was applied AND resets state.
	// The generated column is column-of-a-truncated-table so no
	// separate FTS-store TRUNCATE is needed (unlike SQLite's
	// separate FTS5 virtual table).
	_, err = store.db.ExecContext(ctx, `TRUNCATE TABLE sessions`)
	require.NoError(t, err)
	return store, ctx
}

// saveSession is a small helper that lets each test seed the
// sessions table with a title + user message body so full-text
// queries have something to hit. Uses the canonical Store.Save
// path — that's where the JSON-serialisation shape lives, and we
// want tsvector to index whatever Save actually wrote to payload.
func saveSession(t *testing.T, store *Store, ctx context.Context, title, body string) *agent.Session {
	t.Helper()
	sess := &agent.Session{
		ID:        uuid.NewString(),
		Title:     title,
		Messages:  []agent.Message{{Role: agent.RoleUser, Content: []agent.Content{{Kind: agent.ContentText, Text: body}}}},
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	require.NoError(t, store.Save(ctx, sess))
	return sess
}

// -- unit-only (no DB) --

func TestSearchSchema_MentionsExpectedShape(t *testing.T) {
	// Compile-check the const we ship so an accidental rename
	// blows up in fast local tests, not only when a Postgres
	// instance happens to be reachable. The behavioural
	// integration tests below cover query results.
	assert.Contains(t, searchSchema, "ADD COLUMN IF NOT EXISTS search_vector tsvector")
	assert.Contains(t, searchSchema, "GENERATED ALWAYS AS")
	assert.Contains(t, searchSchema, "USING GIN (search_vector)")
	assert.Contains(t, searchSchema, "'english'")
}

func TestSearch_EmptyQueryRejected(t *testing.T) {
	// Empty-query error surface matches the SQLite driver so
	// callers don't need per-driver branches for the "user
	// pressed enter without typing" case. No Postgres required.
	store := &Store{}
	_, err := store.Search(context.Background(), "  ", SearchOptions{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty search query")
}

// -- integration --

func TestIntegration_SearchRoundTrip(t *testing.T) {
	store, ctx := openSearchStore(t)

	alice := saveSession(t, store, ctx, "kubernetes upgrade plan",
		"we should upgrade the cluster to 1.29 next week and drain nodes gracefully")
	_ = saveSession(t, store, ctx, "postgres migration",
		"schema migration for the orders table, no k8s involved")

	hits, err := store.Search(ctx, "kubernetes", SearchOptions{})
	require.NoError(t, err)
	require.Len(t, hits, 1, "one session mentions kubernetes")
	assert.Equal(t, alice.ID, hits[0].SessionID)
	assert.Equal(t, "kubernetes upgrade plan", hits[0].Title)
	assert.NotEmpty(t, hits[0].Snippet, "ts_headline must return a snippet")
}

func TestIntegration_SearchTitleOutranksBody(t *testing.T) {
	// Titles are weighted 'A', bodies weighted 'B'. A hit in the
	// title must outrank a hit only in the body — matches the
	// SQLite FTS5 setup where the title column is boosted via
	// bm25's default column weighting.
	store, ctx := openSearchStore(t)

	body := saveSession(t, store, ctx, "misc notes",
		"discussion of kubernetes upgrade plans and drain semantics")
	title := saveSession(t, store, ctx, "kubernetes",
		"scheduling notes, not much detail")

	hits, err := store.Search(ctx, "kubernetes", SearchOptions{})
	require.NoError(t, err)
	require.Len(t, hits, 2)
	assert.Equal(t, title.ID, hits[0].SessionID, "title match outranks body match")
	assert.Equal(t, body.ID, hits[1].SessionID)
}

func TestIntegration_SearchStemmingActive(t *testing.T) {
	// 'english' text search config uses the Snowball stemmer, so
	// a query for "running" must match a doc that only says
	// "runs". Pins that we didn't accidentally ship 'simple' or
	// forget the language identifier.
	store, ctx := openSearchStore(t)
	saveSession(t, store, ctx, "morning routine", "she runs three miles every day")

	hits, err := store.Search(ctx, "running", SearchOptions{})
	require.NoError(t, err)
	require.Len(t, hits, 1, "stemmer must fold running → runs")
}

func TestIntegration_SearchWebsyntaxNegation(t *testing.T) {
	// websearch_to_tsquery treats `-foo` as "exclude foo". Pin
	// so a caller relying on it (chat command syntax, CLI flag)
	// keeps working across driver upgrades.
	store, ctx := openSearchStore(t)
	keep := saveSession(t, store, ctx, "kubernetes prod",
		"cluster upgrade plan for production")
	_ = saveSession(t, store, ctx, "kubernetes staging",
		"cluster upgrade plan for staging")

	hits, err := store.Search(ctx, "kubernetes -staging", SearchOptions{})
	require.NoError(t, err)
	require.Len(t, hits, 1)
	assert.Equal(t, keep.ID, hits[0].SessionID)
}

func TestIntegration_SearchRespectsLimit(t *testing.T) {
	// The limit clause must trim the row set BEFORE ts_headline
	// runs — otherwise a large table would headline every row
	// even though only Limit end up returned. Behavioural pin:
	// with 5 docs and Limit=2 we get exactly 2 hits.
	store, ctx := openSearchStore(t)
	for i := 0; i < 5; i++ {
		saveSession(t, store, ctx, "note", "kubernetes topic body")
	}

	hits, err := store.Search(ctx, "kubernetes", SearchOptions{Limit: 2})
	require.NoError(t, err)
	assert.Len(t, hits, 2)
}

func TestIntegration_SearchNoMatchReturnsEmpty(t *testing.T) {
	// A query with no hits returns an empty slice + nil error,
	// NOT sql.ErrNoRows. Matches the SQLite driver's contract
	// so caller reduce-loops don't need per-driver branches.
	store, ctx := openSearchStore(t)
	saveSession(t, store, ctx, "topic", "totally unrelated content")

	hits, err := store.Search(ctx, "kubernetes", SearchOptions{})
	require.NoError(t, err)
	assert.Empty(t, hits)
}

func TestIntegration_SearchUpdatedRowReflectsInIndex(t *testing.T) {
	// Generated columns are re-computed on UPDATE automatically
	// — no trigger needed. Pin the behaviour: rename a session
	// after save, search for the new title, get a hit.
	store, ctx := openSearchStore(t)
	sess := saveSession(t, store, ctx, "old-title", "body irrelevant")
	sess.Title = "postgres migration"
	require.NoError(t, store.Save(ctx, sess))

	hits, err := store.Search(ctx, "migration", SearchOptions{})
	require.NoError(t, err)
	require.Len(t, hits, 1)
	assert.Equal(t, sess.ID, hits[0].SessionID)
	assert.Equal(t, "postgres migration", hits[0].Title)
}

func TestIntegration_RecentSessionsNewestFirst(t *testing.T) {
	store, ctx := openSearchStore(t)
	first := saveSession(t, store, ctx, "first", "body")
	time.Sleep(20 * time.Millisecond) // enough resolution for TIMESTAMPTZ ordering
	second := saveSession(t, store, ctx, "second", "body")

	recent, err := store.RecentSessions(ctx, 10)
	require.NoError(t, err)
	require.Len(t, recent, 2)
	assert.Equal(t, second.ID, recent[0].ID)
	assert.Equal(t, first.ID, recent[1].ID)
}
