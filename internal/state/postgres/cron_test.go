package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// openCronTest opens a CronStore against the CI/dev Postgres
// and truncates the cron_jobs table so each test starts from a
// clean slate. Guarded on ROUSSEAU_TEST_POSTGRES_URL like the
// sessions integration tests.
func openCronTest(t *testing.T) *CronStore {
	t.Helper()
	ctx := context.Background()
	store, err := Open(ctx, requirePG(t))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() }) //nolint:errcheck // test cleanup

	c, err := NewCronStore(ctx, store)
	require.NoError(t, err)
	// TRUNCATE proves the schema was applied AND resets state.
	_, err = store.db.ExecContext(ctx, `TRUNCATE TABLE cron_jobs`)
	require.NoError(t, err)
	return c
}

// -- unit-only (no DB) --

func TestNewCronStore_SchemaIdempotent(t *testing.T) {
	// Compile-check the const is what we ship — an accidental
	// rename would cause the daemon to fail schema-apply on
	// upgrade. Fast local test regardless of Postgres.
	assert.Contains(t, cronSchema, "CREATE TABLE IF NOT EXISTS cron_jobs")
	assert.Contains(t, cronSchema, "TIMESTAMPTZ")
	assert.Contains(t, cronSchema, "BOOLEAN NOT NULL DEFAULT TRUE")
}

// -- integration --

func TestIntegration_CronPutListRoundtrip(t *testing.T) {
	c := openCronTest(t)
	ctx := context.Background()

	j := CronJob{
		ID:        uuid.NewString(),
		Name:      "hourly-status",
		CronExpr:  "0 * * * *",
		Prompt:    "post the daily status update",
		DeliverTo: "+15551234567@s.whatsapp.net",
		Enabled:   true,
	}
	require.NoError(t, c.Put(ctx, j))

	got, err := c.List(ctx)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, j.Name, got[0].Name)
	assert.Equal(t, j.CronExpr, got[0].CronExpr)
	assert.True(t, got[0].Enabled)
	assert.False(t, got[0].CreatedAt.IsZero(), "created_at must default to now")
	assert.Nil(t, got[0].LastRunAt, "unrun jobs have nil last_run_at")
}

func TestIntegration_CronPutValidatesRequiredFields(t *testing.T) {
	c := openCronTest(t)
	// Same error surface as the SQLite driver.
	assert.Error(t, c.Put(context.Background(), CronJob{}))
	assert.Error(t, c.Put(context.Background(), CronJob{ID: "x"}))
	assert.Error(t, c.Put(context.Background(), CronJob{ID: "x", Name: "n"}))
	assert.Error(t, c.Put(context.Background(), CronJob{ID: "x", Name: "n", CronExpr: "e"}))
}

func TestIntegration_CronDuplicateNameRejected(t *testing.T) {
	// UNIQUE(name) constraint round-trip — matches the SQLite
	// driver's guarantee so operator scripts that catch dup
	// errors work with either driver.
	c := openCronTest(t)
	ctx := context.Background()
	first := CronJob{ID: uuid.NewString(), Name: "duplicate", CronExpr: "* * * * *", Prompt: "x"}
	require.NoError(t, c.Put(ctx, first))

	second := CronJob{ID: uuid.NewString(), Name: "duplicate", CronExpr: "* * * * *", Prompt: "y"}
	require.Error(t, c.Put(ctx, second))
}

func TestIntegration_CronListOrderNewestFirst(t *testing.T) {
	c := openCronTest(t)
	ctx := context.Background()

	// Sleep between inserts so created_at ordering is
	// deterministic even under low-resolution clocks.
	first := CronJob{ID: uuid.NewString(), Name: "old", CronExpr: "* * * * *", Prompt: "a"}
	require.NoError(t, c.Put(ctx, first))
	time.Sleep(10 * time.Millisecond)
	second := CronJob{ID: uuid.NewString(), Name: "new", CronExpr: "* * * * *", Prompt: "b"}
	require.NoError(t, c.Put(ctx, second))

	got, err := c.List(ctx)
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, "new", got[0].Name, "newest first")
}

func TestIntegration_CronDeleteByIDAndByName(t *testing.T) {
	// Delete accepts EITHER id or name — the SQLite driver
	// has the same overload so operator ergonomics (`cron
	// remove <name>` vs `cron remove <uuid>`) survive.
	c := openCronTest(t)
	ctx := context.Background()

	j1 := CronJob{ID: uuid.NewString(), Name: "by-id", CronExpr: "* * * * *", Prompt: "x"}
	j2 := CronJob{ID: uuid.NewString(), Name: "by-name", CronExpr: "* * * * *", Prompt: "y"}
	require.NoError(t, c.Put(ctx, j1))
	require.NoError(t, c.Put(ctx, j2))

	// Delete by ID.
	require.NoError(t, c.Delete(ctx, j1.ID))
	// Delete by name.
	require.NoError(t, c.Delete(ctx, "by-name"))

	got, err := c.List(ctx)
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestIntegration_CronDeleteMissingIsNoop(t *testing.T) {
	c := openCronTest(t)
	// Idempotent — matches SQLite so retry-safe scripts work.
	assert.NoError(t, c.Delete(context.Background(), "does-not-exist"))
}

func TestIntegration_CronSetEnabledToggles(t *testing.T) {
	c := openCronTest(t)
	ctx := context.Background()
	j := CronJob{ID: uuid.NewString(), Name: "toggle", CronExpr: "* * * * *", Prompt: "x", Enabled: true}
	require.NoError(t, c.Put(ctx, j))

	require.NoError(t, c.SetEnabled(ctx, "toggle", false))
	got, err := c.List(ctx)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.False(t, got[0].Enabled)

	require.NoError(t, c.SetEnabled(ctx, j.ID, true))
	got, err = c.List(ctx)
	require.NoError(t, err)
	assert.True(t, got[0].Enabled)
}

func TestIntegration_CronRecordRunStampsLastRunAt(t *testing.T) {
	c := openCronTest(t)
	ctx := context.Background()
	j := CronJob{ID: uuid.NewString(), Name: "runme", CronExpr: "* * * * *", Prompt: "x"}
	require.NoError(t, c.Put(ctx, j))

	at := time.Now().UTC().Truncate(time.Second)
	require.NoError(t, c.RecordRun(ctx, j.ID, at))

	got, err := c.List(ctx)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.NotNil(t, got[0].LastRunAt)
	// Postgres TIMESTAMPTZ has microsecond precision; compare
	// on second boundaries to avoid flakes from truncation.
	assert.True(t, got[0].LastRunAt.Truncate(time.Second).Equal(at),
		"last_run_at round-trip: got %s want %s", got[0].LastRunAt, at)
}

// -- LastRunAt UTC normalisation --

func TestIntegration_CronLastRunAtReturnedUTC(t *testing.T) {
	// Property: however the caller supplied the timestamp,
	// List returns UTC. Prevents downstream reporting bugs
	// where the operator sees "8am local" for a UTC-scheduled
	// job.
	c := openCronTest(t)
	ctx := context.Background()
	j := CronJob{ID: uuid.NewString(), Name: "tz-test", CronExpr: "* * * * *", Prompt: "x"}
	require.NoError(t, c.Put(ctx, j))

	local, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Skip("Los_Angeles tz not available in test image; skipping")
	}
	at := time.Date(2026, 1, 15, 10, 30, 0, 0, local)
	require.NoError(t, c.RecordRun(ctx, j.ID, at))

	got, err := c.List(ctx)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.NotNil(t, got[0].LastRunAt)
	assert.Equal(t, time.UTC, got[0].LastRunAt.Location(), "LastRunAt must always be UTC")
}
