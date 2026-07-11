package cron

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
	sqlitestore "github.com/sebastienrousseau/rousseau-agent/internal/state/sqlite"
)

func silentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func openTestStore(t *testing.T) (*sqlitestore.Store, *sqlitestore.CronStore) {
	t.Helper()
	// File-backed (not :memory:) so pool connections share the schema.
	path := filepath.Join(t.TempDir(), "cron.db")
	ctx := context.Background()
	s, err := sqlitestore.Open(ctx, path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	cs, err := sqlitestore.NewCronStore(ctx, s)
	require.NoError(t, err)
	return s, cs
}

type stubRunner struct {
	mu      sync.Mutex
	prompts []string
	reply   string
	err     error
}

func (s *stubRunner) RunOnce(_ context.Context, prompt string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prompts = append(s.prompts, prompt)
	return s.reply, s.err
}

func (s *stubRunner) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.prompts)
}

type stubDelivery struct {
	mu      sync.Mutex
	targets []string
	bodies  []string
	err     error
}

func (d *stubDelivery) fn(_ context.Context, target, body string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.targets = append(d.targets, target)
	d.bodies = append(d.bodies, body)
	return d.err
}

func TestScheduler_FiresEnabledJob(t *testing.T) {
	_, cs := openTestStore(t)
	require.NoError(t, cs.Put(context.Background(), sqlitestore.CronJob{
		ID:        "job-1",
		Name:      "test-job",
		CronExpr:  "@every 100ms",
		Prompt:    "hello",
		DeliverTo: "1@s.whatsapp.net",
		Enabled:   true,
	}))

	runner := &stubRunner{reply: "hi back"}
	del := &stubDelivery{}
	s := New(Config{
		Store:        cs,
		Runner:       runner,
		Delivery:     del.fn,
		PollInterval: 50 * time.Millisecond,
		Logger:       silentLogger(),
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, s.Start(ctx))
	defer func() {
		sctx, c := context.WithTimeout(context.Background(), time.Second)
		defer c()
		_ = s.Shutdown(sctx)
	}()

	// Wait up to 1s for the first fire.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && runner.count() == 0 {
		time.Sleep(20 * time.Millisecond)
	}
	assert.GreaterOrEqual(t, runner.count(), 1)
	assert.Contains(t, runner.prompts, "hello")
	del.mu.Lock()
	assert.Contains(t, del.targets, "1@s.whatsapp.net")
	assert.Contains(t, del.bodies, "hi back")
	del.mu.Unlock()
}

func TestScheduler_SkipsDisabledJob(t *testing.T) {
	_, cs := openTestStore(t)
	require.NoError(t, cs.Put(context.Background(), sqlitestore.CronJob{
		ID: "job-x", Name: "off", CronExpr: "@every 100ms",
		Prompt: "shouldnt run", Enabled: false,
	}))

	runner := &stubRunner{reply: "ok"}
	s := New(Config{Store: cs, Runner: runner, PollInterval: 50 * time.Millisecond, Logger: silentLogger()})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, s.Start(ctx))
	defer func() {
		sctx, c := context.WithTimeout(context.Background(), time.Second)
		defer c()
		_ = s.Shutdown(sctx)
	}()
	time.Sleep(300 * time.Millisecond)
	assert.Equal(t, 0, runner.count())
}

func TestScheduler_ToggleEnableIsPickedUp(t *testing.T) {
	_, cs := openTestStore(t)
	require.NoError(t, cs.Put(context.Background(), sqlitestore.CronJob{
		ID: "job-tog", Name: "toggle", CronExpr: "@every 80ms",
		Prompt: "go", Enabled: false,
	}))

	runner := &stubRunner{reply: "done"}
	s := New(Config{Store: cs, Runner: runner, PollInterval: 40 * time.Millisecond, Logger: silentLogger()})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, s.Start(ctx))
	defer func() {
		sctx, c := context.WithTimeout(context.Background(), time.Second)
		defer c()
		_ = s.Shutdown(sctx)
	}()
	time.Sleep(120 * time.Millisecond)
	require.Equal(t, 0, runner.count(), "disabled job must not fire")

	require.NoError(t, cs.SetEnabled(context.Background(), "job-tog", true))
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && runner.count() == 0 {
		time.Sleep(20 * time.Millisecond)
	}
	assert.GreaterOrEqual(t, runner.count(), 1)
}

func TestScheduler_RunnerErrorLoggedNotFatal(t *testing.T) {
	_, cs := openTestStore(t)
	require.NoError(t, cs.Put(context.Background(), sqlitestore.CronJob{
		ID: "job-err", Name: "boom", CronExpr: "@every 100ms",
		Prompt: "explode", Enabled: true,
	}))
	runner := &stubRunner{err: errors.New("no")}
	s := New(Config{Store: cs, Runner: runner, PollInterval: 40 * time.Millisecond, Logger: silentLogger()})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, s.Start(ctx))
	defer func() {
		sctx, c := context.WithTimeout(context.Background(), time.Second)
		defer c()
		_ = s.Shutdown(sctx)
	}()
	// Wait actively — CI schedulers can add jitter.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && runner.count() == 0 {
		time.Sleep(30 * time.Millisecond)
	}
	assert.GreaterOrEqual(t, runner.count(), 1, "scheduler should retry even after errors")
}

func TestScheduler_ShutdownIdempotent(t *testing.T) {
	_, cs := openTestStore(t)
	s := New(Config{Store: cs, Runner: &stubRunner{}, Logger: silentLogger()})
	require.NoError(t, s.Start(context.Background()))
	require.NoError(t, s.Shutdown(context.Background()))
	require.NoError(t, s.Shutdown(context.Background()))
}

type stubProvider struct {
	reply string
	err   error
}

func (s *stubProvider) Name() string { return "stub" }
func (s *stubProvider) Complete(_ context.Context, _ agent.Request) (agent.Response, error) {
	if s.err != nil {
		return agent.Response{}, s.err
	}
	return agent.Response{
		Message: agent.Message{
			Role:    agent.RoleAssistant,
			Content: []agent.Content{{Kind: agent.ContentText, Text: s.reply}},
		},
		StopReason: agent.StopEndTurn,
	}, nil
}

func TestProviderRunner_RunOnce(t *testing.T) {
	r := &ProviderRunner{Provider: &stubProvider{reply: "hello"}}
	got, err := r.RunOnce(context.Background(), "prompt")
	require.NoError(t, err)
	assert.Equal(t, "hello", got)
}

func TestProviderRunner_NilProvider(t *testing.T) {
	r := &ProviderRunner{}
	_, err := r.RunOnce(context.Background(), "prompt")
	assert.Error(t, err)
}

func TestProviderRunner_ProviderError(t *testing.T) {
	r := &ProviderRunner{Provider: &stubProvider{err: errors.New("api down")}}
	_, err := r.RunOnce(context.Background(), "prompt")
	assert.Error(t, err)
}

func TestProviderRunner_EmptyTextIsAllowed(t *testing.T) {
	r := &ProviderRunner{Provider: &stubProvider{reply: ""}}
	got, err := r.RunOnce(context.Background(), "p")
	require.NoError(t, err)
	assert.Equal(t, "", got)
}
