package cron

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	sqlitestore "github.com/sebastienrousseau/rousseau-agent/internal/state/sqlite"
)

// syncBuffer is a concurrency-safe log sink: the poll loop and cron
// jobs log from their own goroutines while the test reads.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// capturingLogger returns a logger plus the buffer it writes to, so
// tests can assert on the events the scheduler emits when a
// collaborator fails.
func capturingLogger() (*slog.Logger, *syncBuffer) {
	buf := &syncBuffer{}
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})), buf
}

func TestNew_DefaultsLoggerAndPollInterval(t *testing.T) {
	s := New(Config{})
	require.NotNil(t, s.logger)
	assert.Equal(t, 60*time.Second, s.cfg.PollInterval)
}

func TestScheduler_StartFailsWhenJobCatalogueIsUnreadable(t *testing.T) {
	store, cs := openTestStore(t)
	require.NoError(t, store.Close())

	s := New(Config{Store: cs, Runner: &stubRunner{}, Logger: silentLogger()})
	err := s.Start(context.Background())
	require.Error(t, err)
	assert.Empty(t, s.entries, "a failed sync must not leave live entries behind")
}

func TestScheduler_SyncDropsEntryWhenJobIsDisabled(t *testing.T) {
	ctx := context.Background()
	_, cs := openTestStore(t)
	require.NoError(t, cs.Put(ctx, sqlitestore.CronJob{
		ID: "job-1", Name: "nightly", CronExpr: "@every 1h",
		Prompt: "p", Enabled: true,
	}))

	s := New(Config{Store: cs, Runner: &stubRunner{}, Logger: silentLogger()})
	require.NoError(t, s.sync(ctx))
	require.Len(t, s.entries, 1)

	require.NoError(t, cs.SetEnabled(ctx, "job-1", false))
	require.NoError(t, s.sync(ctx))
	assert.Empty(t, s.entries, "disabling a job must retire its cron entry")

	// Re-enabling brings it back — the reconcile is symmetric.
	require.NoError(t, cs.SetEnabled(ctx, "job-1", true))
	require.NoError(t, s.sync(ctx))
	assert.Len(t, s.entries, 1)

	// Deleting it retires the entry too.
	require.NoError(t, cs.Delete(ctx, "job-1"))
	require.NoError(t, s.sync(ctx))
	assert.Empty(t, s.entries)
}

func TestScheduler_SyncSkipsJobWithUnparseableExpression(t *testing.T) {
	ctx := context.Background()
	_, cs := openTestStore(t)
	require.NoError(t, cs.Put(ctx, sqlitestore.CronJob{
		ID: "bad", Name: "broken", CronExpr: "every other tuesday",
		Prompt: "p", Enabled: true,
	}))
	require.NoError(t, cs.Put(ctx, sqlitestore.CronJob{
		ID: "good", Name: "fine", CronExpr: "@every 1h",
		Prompt: "p", Enabled: true,
	}))

	logger, logs := capturingLogger()
	s := New(Config{Store: cs, Runner: &stubRunner{}, Logger: logger})

	// One unschedulable job must not abort the whole sync.
	require.NoError(t, s.sync(ctx))
	assert.Contains(t, logs.String(), "cron.schedule_failed")
	require.Len(t, s.entries, 1)
	_, ok := s.entries["good"]
	assert.True(t, ok, "the well-formed job still gets scheduled")
}

func TestScheduler_FireSkipsRecordRunWhenDeliveryFails(t *testing.T) {
	ctx := context.Background()
	_, cs := openTestStore(t)
	job := sqlitestore.CronJob{
		ID: "job-1", Name: "nightly", CronExpr: "@every 1h",
		Prompt: "p", DeliverTo: "1@s.whatsapp.net", Enabled: true,
	}
	require.NoError(t, cs.Put(ctx, job))

	del := &stubDelivery{err: errors.New("transport down")}
	logger, logs := capturingLogger()
	s := New(Config{Store: cs, Runner: &stubRunner{reply: "hi"}, Delivery: del.fn, Logger: logger})

	s.fire(job)

	assert.Contains(t, logs.String(), "cron.delivery_failed")
	assert.NotContains(t, logs.String(), "cron.completed")

	// An undelivered run must not be stamped as run, so the next tick
	// retries it.
	jobs, err := cs.List(ctx)
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	assert.Nil(t, jobs[0].LastRunAt)
}

func TestScheduler_FireCompletesEvenWhenRunCannotBeRecorded(t *testing.T) {
	store, cs := openTestStore(t)
	job := sqlitestore.CronJob{
		ID: "job-1", Name: "nightly", CronExpr: "@every 1h", Prompt: "p", Enabled: true,
	}
	require.NoError(t, cs.Put(context.Background(), job))
	require.NoError(t, store.Close())

	runner := &stubRunner{reply: "hi"}
	logger, logs := capturingLogger()
	s := New(Config{Store: cs, Runner: runner, Logger: logger})

	s.fire(job)

	assert.Equal(t, 1, runner.count())
	assert.Contains(t, logs.String(), "cron.record_failed")
	assert.Contains(t, logs.String(), "cron.completed",
		"a bookkeeping failure must not mask a successful run")
}

func TestScheduler_ShutdownHonoursCallerDeadline(t *testing.T) {
	s := New(Config{Runner: &stubRunner{}, Logger: silentLogger()})

	var once sync.Once
	started := make(chan struct{})
	release := make(chan struct{})
	_, err := s.cron.AddFunc("@every 1s", func() {
		once.Do(func() { close(started) })
		<-release
	})
	require.NoError(t, err)
	s.cron.Start()

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("job never fired")
	}

	// The in-flight job holds the cron wait group open, so Shutdown
	// must give up on the caller's cancelled context rather than block.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	assert.ErrorIs(t, s.Shutdown(ctx), context.Canceled)

	close(release)
}

func TestScheduler_PollLoopKeepsRunningWhenResyncFails(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	store, cs := openTestStore(t)
	require.NoError(t, cs.Put(ctx, sqlitestore.CronJob{
		ID: "job-1", Name: "nightly", CronExpr: "@every 1h", Prompt: "p", Enabled: true,
	}))

	logger, logs := capturingLogger()
	s := New(Config{
		Store:        cs,
		Runner:       &stubRunner{},
		PollInterval: 20 * time.Millisecond,
		Logger:       logger,
	})
	require.NoError(t, s.Start(ctx))
	t.Cleanup(func() {
		sctx, c := context.WithTimeout(context.Background(), time.Second)
		defer c()
		_ = s.Shutdown(sctx) //nolint:errcheck // test cleanup
	})

	// Pull the catalogue out from under the running poll loop: the
	// next tick's re-sync fails and must be logged, not fatal.
	require.NoError(t, store.Close())

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && !strings.Contains(logs.String(), "cron.sync_failed") {
		time.Sleep(10 * time.Millisecond)
	}
	assert.Contains(t, logs.String(), "cron.sync_failed")

	// The entry scheduled before the failure is still live.
	s.mu.Lock()
	entries := len(s.entries)
	s.mu.Unlock()
	assert.Equal(t, 1, entries)
}
