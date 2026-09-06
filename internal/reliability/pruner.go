package reliability

import (
	"context"
	"log/slog"
	"time"
)

// Pruner is the narrow surface RunPruner drives — anything that
// can DELETE samples older than a cutoff. The SQLite +
// (eventual) Postgres reliability stores both satisfy this
// signature. Kept as an interface so the retention loop can be
// tested against a fake without opening a real DB.
type Pruner interface {
	PruneBefore(ctx context.Context, cutoff time.Time) (int64, error)
}

// PruneConfig tunes the retention loop launched by RunPruner.
// Zero-value picks the daemon defaults documented on each field.
type PruneConfig struct {
	// Retention is how long to keep samples before pruning.
	// Zero uses 30 days — the paper's default rolling window
	// (§3) and the CLI's largest --window preset.
	Retention time.Duration
	// Interval is how often the pruner wakes to check. Zero
	// uses 6 hours: pruning once every ~5-min-worth-of-samples
	// (a busy daemon at 10 turns/minute produces ~14k samples
	// in 24h; the ring already caps memory, so this only
	// governs disk space).
	Interval time.Duration
	// Logger receives INFO on successful prune + WARN on
	// failure. Nil uses slog.Default.
	Logger *slog.Logger
	// Now is the injectable clock — tests substitute a fixed
	// time. Nil uses time.Now.
	Now func() time.Time
	// Tick is the injectable ticker channel — tests drive it
	// synchronously. Nil uses time.NewTicker(Interval).C.
	Tick <-chan time.Time
}

// RunPruner runs the retention loop until ctx is cancelled. Meant
// to be launched in a goroutine at daemon startup:
//
//	go RunPruner(ctx, store, PruneConfig{})
//
// Fire-and-forget errors: every failed prune logs at WARN and the
// loop continues. A long-lived DB lock or corruption does not
// wedge the daemon — retention is best-effort.
//
// Returns nothing. Blocks until ctx is Done or Tick is closed.
// Deterministic under context cancellation: the current in-flight
// PruneBefore call completes before the function returns.
func RunPruner(ctx context.Context, p Pruner, cfg PruneConfig) {
	if p == nil {
		return
	}
	if cfg.Retention <= 0 {
		cfg.Retention = 30 * 24 * time.Hour
	}
	if cfg.Interval <= 0 {
		cfg.Interval = 6 * time.Hour
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	tick := cfg.Tick
	if tick == nil {
		t := time.NewTicker(cfg.Interval)
		defer t.Stop()
		tick = t.C
	}

	// Run once immediately on entry so operators see disk-usage
	// benefits without waiting a full Interval on first boot.
	pruneOnce(ctx, p, cfg)

	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-tick:
			if !ok {
				return
			}
			pruneOnce(ctx, p, cfg)
		}
	}
}

// pruneOnce runs a single prune iteration + logs the outcome.
// Extracted so RunPruner's select loop stays legible.
func pruneOnce(ctx context.Context, p Pruner, cfg PruneConfig) {
	cutoff := cfg.Now().Add(-cfg.Retention)
	n, err := p.PruneBefore(ctx, cutoff)
	if err != nil {
		cfg.Logger.Warn("reliability.prune_failed",
			slog.Duration("retention", cfg.Retention),
			slog.String("err", err.Error()),
		)
		return
	}
	if n > 0 {
		cfg.Logger.Info("reliability.pruned",
			slog.Int64("rows", n),
			slog.Duration("retention", cfg.Retention),
		)
	}
}
