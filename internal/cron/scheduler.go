// Package cron runs stored CronJob entries on their configured
// schedule, executes their prompt through an agent.Provider, and
// hands the reply to a transport-agnostic Delivery function.
//
// The scheduler is designed to be started by a long-running daemon
// (rousseau whatsapp) alongside a Sender it can deliver through, but
// nothing in this package imports internal/transport — the delivery
// contract is a single function type.
package cron

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	rcron "github.com/robfig/cron/v3"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
	sqlitestore "github.com/sebastienrousseau/rousseau-agent/internal/state/sqlite"
)

// Delivery is called with the destination identifier (a WhatsApp JID,
// a Telegram chat id — the meaning is transport-specific) and the
// finished reply. Returning an error is logged; the scheduler retries
// on the next tick, not immediately.
type Delivery func(ctx context.Context, target, body string) error

// TurnRunner runs one prompt against a fresh Session and returns the
// final assistant text. Provided so the scheduler stays independent
// of agent.Agent's exact shape.
type TurnRunner interface {
	// RunOnce executes a single prompt as if it were the sole user
	// message in a fresh session. Implementations own the provider
	// interaction and return the final assistant text.
	RunOnce(ctx context.Context, prompt string) (string, error)
}

// Config bundles the scheduler's collaborators.
type Config struct {
	// Store is the persistent job catalogue.
	Store *sqlitestore.CronStore
	// Runner executes a single prompt.
	Runner TurnRunner
	// Delivery ships the reply to the configured target. Nil disables
	// delivery — useful for tests that only care that runs fired.
	Delivery Delivery
	// PollInterval controls how often the scheduler re-syncs the job
	// list from the store. Zero uses 60s. New jobs created via the CLI
	// become live within one poll interval.
	PollInterval time.Duration
	// Logger receives structured events. Nil uses slog.Default().
	Logger *slog.Logger
}

// Scheduler is the running cron loop.
type Scheduler struct {
	cfg     Config
	logger  *slog.Logger
	cron    *rcron.Cron
	mu      sync.Mutex
	entries map[string]rcron.EntryID // jobID → entry id
	stopped bool
}

// New constructs a Scheduler. Call Start to launch the goroutines.
func New(cfg Config) *Scheduler {
	if cfg.PollInterval == 0 {
		cfg.PollInterval = 60 * time.Second
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Scheduler{
		cfg:     cfg,
		logger:  cfg.Logger,
		cron:    rcron.New(),
		entries: map[string]rcron.EntryID{},
	}
}

// Start begins ticking. It blocks only briefly to sync the initial job
// list from the store; the actual scheduling runs on a background
// goroutine owned by the underlying cron library. Stop when ctx is
// cancelled or Shutdown is called.
func (s *Scheduler) Start(ctx context.Context) error {
	if err := s.sync(ctx); err != nil {
		return err
	}
	s.cron.Start()
	go s.pollLoop(ctx)
	s.logger.Info("cron.started", slog.Duration("poll_interval", s.cfg.PollInterval))
	return nil
}

// Shutdown stops firing jobs and waits for in-flight runs to finish.
// Safe to call multiple times.
func (s *Scheduler) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return nil
	}
	s.stopped = true
	s.mu.Unlock()

	done := s.cron.Stop()
	select {
	case <-done.Done():
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Scheduler) pollLoop(ctx context.Context) {
	t := time.NewTicker(s.cfg.PollInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := s.sync(ctx); err != nil {
				s.logger.Warn("cron.sync_failed", slog.String("err", err.Error()))
			}
		}
	}
}

// sync reconciles the running cron entries with the job table. Enabled
// jobs get a live entry; disabled or deleted ones lose theirs.
func (s *Scheduler) sync(ctx context.Context) error {
	jobs, err := s.cfg.Store.List(ctx)
	if err != nil {
		return err
	}

	live := make(map[string]sqlitestore.CronJob, len(jobs))
	for _, j := range jobs {
		if j.Enabled {
			live[j.ID] = j
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Remove entries whose job disappeared or was disabled.
	for id, entry := range s.entries {
		if _, keep := live[id]; !keep {
			s.cron.Remove(entry)
			delete(s.entries, id)
		}
	}

	// Add entries for jobs newly enabled or seen for the first time.
	for id, job := range live {
		if _, exists := s.entries[id]; exists {
			continue
		}
		entryID, err := s.cron.AddFunc(job.CronExpr, func() {
			s.fire(job)
		})
		if err != nil {
			s.logger.Warn("cron.schedule_failed",
				slog.String("job", job.Name),
				slog.String("expr", job.CronExpr),
				slog.String("err", err.Error()))
			continue
		}
		s.entries[id] = entryID
		s.logger.Info("cron.scheduled",
			slog.String("job", job.Name),
			slog.String("expr", job.CronExpr))
	}
	return nil
}

// fire runs a single scheduled job. It creates its own bounded context
// so a stuck provider does not block the whole scheduler.
func (s *Scheduler) fire(job sqlitestore.CronJob) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	start := time.Now()
	s.logger.Info("cron.firing", slog.String("job", job.Name))

	reply, err := s.cfg.Runner.RunOnce(ctx, job.Prompt)
	elapsed := time.Since(start)
	if err != nil {
		s.logger.Error("cron.run_failed",
			slog.String("job", job.Name),
			slog.String("err", err.Error()),
			slog.Duration("elapsed", elapsed))
		return
	}

	if s.cfg.Delivery != nil && job.DeliverTo != "" && reply != "" {
		if err := s.cfg.Delivery(ctx, job.DeliverTo, reply); err != nil {
			s.logger.Error("cron.delivery_failed",
				slog.String("job", job.Name),
				slog.String("target", job.DeliverTo),
				slog.String("err", err.Error()))
			return
		}
	}

	if err := s.cfg.Store.RecordRun(ctx, job.ID, time.Now().UTC()); err != nil {
		s.logger.Warn("cron.record_failed",
			slog.String("job", job.Name),
			slog.String("err", err.Error()))
	}
	s.logger.Info("cron.completed",
		slog.String("job", job.Name),
		slog.Duration("elapsed", elapsed),
		slog.Int("reply_len", len(reply)))
}

// ProviderRunner is the default TurnRunner: it wraps an agent.Provider
// and runs each cron prompt as a one-shot Session with a synthetic
// session id.
type ProviderRunner struct {
	Provider     agent.Provider
	SystemPrompt string
}

// RunOnce satisfies TurnRunner.
func (p *ProviderRunner) RunOnce(ctx context.Context, prompt string) (string, error) {
	if p.Provider == nil {
		return "", errors.New("cron: nil provider")
	}
	sess := agent.NewSession("cron:" + time.Now().UTC().Format("2006-01-02T15:04:05Z"))
	sess.Append(agent.NewUserText(prompt))
	req := agent.Request{
		SessionID: sess.ID,
		System:    p.SystemPrompt,
		Messages:  sess.Messages,
	}
	resp, err := p.Provider.Complete(ctx, req)
	if err != nil {
		return "", fmt.Errorf("cron: provider: %w", err)
	}
	for _, c := range resp.Message.Content {
		if c.Kind == agent.ContentText && c.Text != "" {
			return c.Text, nil
		}
	}
	return "", nil
}
