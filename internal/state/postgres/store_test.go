package postgres

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
	"github.com/sebastienrousseau/rousseau-agent/internal/state"
)

// pgTestEnv is the env var integration tests read a DSN from.
// Absence means "skip the integration tests" — a clean-workstation
// developer can still run the unit-only tests.
const pgTestEnv = "ROUSSEAU_TEST_POSTGRES_URL"

func requirePG(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv(pgTestEnv)
	if dsn == "" {
		t.Skipf("skipping: %s is not set — start a local Postgres and export the DSN to run", pgTestEnv)
	}
	return dsn
}

// openTest opens a Store against the CI/dev Postgres and truncates
// the sessions table so each test starts from a clean slate. The
// TRUNCATE also proves the schema apply on Open is idempotent.
func openTest(t *testing.T) *Store {
	t.Helper()
	ctx := context.Background()
	store, err := Open(ctx, requirePG(t))
	require.NoError(t, err)
	_, err = store.db.ExecContext(ctx, `TRUNCATE TABLE sessions`)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() }) //nolint:errcheck // test cleanup
	return store
}

// -- unit tests (no DB required) -----------------------------------

func TestOpen_EmptyDSNRejected(t *testing.T) {
	_, err := Open(context.Background(), "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty DSN")
}

func TestOpen_UnreachableDSNFailsFast(t *testing.T) {
	// Ping must fail on a DSN that points at a port with no
	// listener — proves the constructor doesn't silently defer
	// the error to the first Save. Use a TCP-shaped DSN so pgx
	// gets past its own URL parse.
	ctx, cancel := context.WithTimeout(context.Background(), pingTimeout)
	defer cancel()
	_, err := Open(ctx, "postgres://user:pass@127.0.0.1:1/no_such_db")
	require.Error(t, err, "unreachable DSN must fail at Open, not at first Save")
}

// pingTimeout keeps the "unreachable DSN" test from hanging
// on a slow OS network stack — the failure path exercises
// Open's Ping guard, not a timeout precision guarantee.
const pingTimeout = 2 * 1000 * 1000 * 1000 // 2 seconds in nanoseconds

// -- integration tests (DB required, skipped otherwise) ------------

func TestIntegration_SaveLoadRoundtrip(t *testing.T) {
	store := openTest(t)
	ctx := context.Background()

	s := agent.NewSession("first")
	s.Append(agent.NewUserText("hello"))

	require.NoError(t, store.Save(ctx, s))

	got, err := store.Load(ctx, s.ID)
	require.NoError(t, err)
	assert.Equal(t, s.ID, got.ID)
	assert.Equal(t, "first", got.Title)
	require.Len(t, got.Messages, 1)
	assert.Equal(t, "hello", got.Messages[0].Content[0].Text)
}

func TestIntegration_LoadMissingReturnsErrNotFound(t *testing.T) {
	store := openTest(t)
	_, err := store.Load(context.Background(), "does-not-exist")
	assert.ErrorIs(t, err, state.ErrNotFound)
}

func TestIntegration_ListOrdersNewestFirst(t *testing.T) {
	store := openTest(t)
	ctx := context.Background()

	for _, title := range []string{"a", "b", "c"} {
		s := agent.NewSession(title)
		require.NoError(t, store.Save(ctx, s))
	}
	sums, err := store.List(ctx, 0)
	require.NoError(t, err)
	assert.Len(t, sums, 3)
	// Newest first — the last-written session ("c") should be at
	// index 0.
	assert.Equal(t, "c", sums[0].Title)
}

func TestIntegration_ListRespectsLimit(t *testing.T) {
	store := openTest(t)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		require.NoError(t, store.Save(ctx, agent.NewSession("s")))
	}
	sums, err := store.List(ctx, 2)
	require.NoError(t, err)
	assert.Len(t, sums, 2)
}

func TestIntegration_DeleteMissingIsNoop(t *testing.T) {
	// Matches the SQLite driver: deleting a session that isn't
	// there is not an error. Callers rely on this for idempotent
	// cleanup.
	store := openTest(t)
	err := store.Delete(context.Background(), "not-a-real-id")
	assert.NoError(t, err)
}

func TestIntegration_UpsertReplacesRow(t *testing.T) {
	// ON CONFLICT DO UPDATE is the load-bearing HA behaviour —
	// two replicas writing the same session must not error and
	// the last writer wins.
	store := openTest(t)
	ctx := context.Background()

	s := agent.NewSession("v1")
	s.Append(agent.NewUserText("first"))
	require.NoError(t, store.Save(ctx, s))

	s.Title = "v2"
	s.Append(agent.NewUserText("second"))
	require.NoError(t, store.Save(ctx, s))

	got, err := store.Load(ctx, s.ID)
	require.NoError(t, err)
	assert.Equal(t, "v2", got.Title)
	assert.Len(t, got.Messages, 2)
}

func TestIntegration_ConcurrentWritesDoNotDeadlock(t *testing.T) {
	// Simulates a small replica fleet: N goroutines writing
	// distinct sessions in parallel. If we hold locks too long
	// or leak a transaction, this hangs.
	store := openTest(t)
	ctx := context.Background()

	const n = 10
	var wg sync.WaitGroup
	wg.Add(n)
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			s := agent.NewSession("concurrent")
			if err := store.Save(ctx, s); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	sums, err := store.List(ctx, 0)
	require.NoError(t, err)
	assert.Len(t, sums, n)
}

func TestIntegration_ListEmptyReturnsNil(t *testing.T) {
	store := openTest(t)
	sums, err := store.List(context.Background(), 0)
	require.NoError(t, err)
	assert.Empty(t, sums, "empty table must return an empty (or nil) slice, not error")
}

// Sanity: the DSN parse rejects obvious junk before it ever
// reaches the network. pgx handles this at sql.Open time; we
// just want to confirm we surface it.
func TestOpen_BadDSNShape(t *testing.T) {
	// pgx's parser accepts a surprising range of strings; the
	// clearest breakage is a scheme it doesn't know.
	_, err := Open(context.Background(), "not-a-url-at-all")
	require.Error(t, err)
	assert.True(t,
		strings.Contains(err.Error(), "postgres:") ||
			strings.Contains(err.Error(), "parse") ||
			strings.Contains(err.Error(), "invalid"),
		"error should identify the DSN as the culprit, got: %v", err)
}
