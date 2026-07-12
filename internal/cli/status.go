package cli

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
)

// StatusReport is the machine-readable status snapshot. The `status`
// command renders it as human-friendly text; other consumers (a future
// Prometheus scraper, an MCP tool) can serialise it directly.
type StatusReport struct {
	Version           string        `json:"version"`
	Commit            string        `json:"commit"`
	BuildDate         string        `json:"build_date"`
	StatePath         string        `json:"state_path"`
	StateSize         int64         `json:"state_size_bytes"`
	Sessions          int           `json:"sessions"`
	CronJobs          int           `json:"cron_jobs"`
	CronEnabled       int           `json:"cron_enabled"`
	JIDMappings       int           `json:"jid_mappings"`
	CachedClaude      int           `json:"cached_claude_sessions"`
	LastActivityAt    time.Time     `json:"last_activity_at"`
	StateReachTimeout time.Duration `json:"state_reach_timeout"`
}

func newStatusCmd(opts *Options) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Print a runtime status snapshot (session count, cron state, DB health)",
		Long: "status is designed to run alongside a live daemon — it opens\n" +
			"the sqlite store in a read-only path and reports the counts an\n" +
			"operator would otherwise dig out of the DB by hand. Safe to\n" +
			"call from `watch`, cron, or a health probe.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path := opts.Config.State.Path
			if path == "" {
				home, err := os.UserHomeDir()
				if err != nil {
					return err
				}
				path = filepath.Join(home, ".local", "share", "rousseau", "sessions.db")
			}
			report, err := collectStatus(cmd.Context(), path)
			if err != nil {
				return err
			}
			renderStatus(cmd.OutOrStdout(), report)
			return nil
		},
	}
}

// collectStatus opens the sqlite store read-only and gathers a
// StatusReport. Every step is defensive: a missing table still
// produces a partial report instead of failing.
func collectStatus(ctx context.Context, path string) (StatusReport, error) {
	report := StatusReport{
		Version:           version,
		Commit:            commit,
		BuildDate:         buildDate,
		StatePath:         path,
		StateReachTimeout: 500 * time.Millisecond,
	}
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return report, nil // pre-first-run — an empty report is fine
		}
		return report, err
	}
	report.StateSize = info.Size()

	// Open read-only so we do not fight the running daemon for the write
	// lock. mode=ro on the DSN keeps modernc.org/sqlite in read-only.
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		return report, err
	}
	defer func() { _ = db.Close() }()

	report.Sessions = scanCount(ctx, db, "SELECT COUNT(*) FROM sessions")
	report.CronJobs = scanCount(ctx, db, "SELECT COUNT(*) FROM cron_jobs")
	report.CronEnabled = scanCount(ctx, db, "SELECT COUNT(*) FROM cron_jobs WHERE enabled = 1")
	report.JIDMappings = scanCount(ctx, db, "SELECT COUNT(*) FROM jid_sessions")
	report.CachedClaude = scanCount(ctx, db, "SELECT COUNT(*) FROM claude_sessions")

	var lastAt sql.NullString
	if err := db.QueryRowContext(ctx, "SELECT MAX(updated_at) FROM sessions").Scan(&lastAt); err == nil && lastAt.Valid {
		if t, err := time.Parse("2006-01-02T15:04:05.000Z", lastAt.String); err == nil {
			report.LastActivityAt = t
		}
	}
	return report, nil
}

// scanCount runs a scalar COUNT query and returns 0 on any error —
// useful because a fresh install may not yet have every table.
func scanCount(ctx context.Context, db *sql.DB, q string) int {
	var n int
	if err := db.QueryRowContext(ctx, q).Scan(&n); err != nil {
		return 0
	}
	return n
}

func renderStatus(w interface {
	Write(p []byte) (int, error)
}, r StatusReport) {
	fmt.Fprintf(w, "rousseau %s (commit %s, built %s)\n", r.Version, r.Commit, r.BuildDate)
	fmt.Fprintf(w, "\n")
	fmt.Fprintf(w, "state.path             %s\n", r.StatePath)
	fmt.Fprintf(w, "state.size             %s\n", humanBytes(r.StateSize))
	fmt.Fprintf(w, "sessions               %d\n", r.Sessions)
	fmt.Fprintf(w, "cron.jobs              %d (enabled=%d)\n", r.CronJobs, r.CronEnabled)
	fmt.Fprintf(w, "transport.jid_mappings %d\n", r.JIDMappings)
	fmt.Fprintf(w, "claude.cached_sessions %d\n", r.CachedClaude)
	if !r.LastActivityAt.IsZero() {
		fmt.Fprintf(w, "last_activity_at       %s (%s ago)\n",
			r.LastActivityAt.Format(time.RFC3339),
			time.Since(r.LastActivityAt).Round(time.Second))
	}
}
