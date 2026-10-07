package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	sqlitestore "github.com/sebastienrousseau/rousseau-agent/internal/state/sqlite"
)

// turnJournalSchema mirrors the SQLite journal with a native
// TIMESTAMPTZ for started_at.
const turnJournalSchema = `
CREATE TABLE IF NOT EXISTS turns_inflight (
    transport  TEXT NOT NULL,
    sender     TEXT NOT NULL,
    preview    TEXT NOT NULL,
    started_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (transport, sender)
);`

// TurnJournal records agent turns in flight so a daemon that restarts
// mid-turn can tell the sender their message was interrupted. With
// several replicas sharing one Postgres, a replica that takes the
// journal at startup notifies every sender whose turn any replica
// cut off; a turn still running on a live replica is journalled
// again when it ends, so the notice is at worst a duplicate rather
// than a silent loss.
type TurnJournal struct{ db *sql.DB }

// InterruptedTurn is a turn that started but never ended.
type InterruptedTurn = sqlitestore.InterruptedTurn

// NewTurnJournal installs the journal table.
func NewTurnJournal(ctx context.Context, s *Store) (*TurnJournal, error) {
	if _, err := s.db.ExecContext(ctx, turnJournalSchema); err != nil {
		return nil, fmt.Errorf("postgres: turn journal: %w", err)
	}
	return &TurnJournal{db: s.db}, nil
}

// previewRunes bounds the stored start of the message.
const previewRunes = 120

// Begin records that sender's turn on transport has started.
func (j *TurnJournal) Begin(ctx context.Context, transport, sender, body string) error {
	preview := strings.Join(strings.Fields(body), " ")
	if r := []rune(preview); len(r) > previewRunes {
		preview = string(r[:previewRunes]) + "…"
	}
	_, err := j.db.ExecContext(ctx, `
INSERT INTO turns_inflight (transport, sender, preview, started_at) VALUES ($1, $2, $3, NOW())
ON CONFLICT (transport, sender) DO UPDATE SET preview = EXCLUDED.preview, started_at = NOW()`,
		transport, sender, preview)
	if err != nil {
		return fmt.Errorf("postgres: turn journal begin: %w", err)
	}
	return nil
}

// End records that sender's turn finished (reply sent or failed).
func (j *TurnJournal) End(ctx context.Context, transport, sender string) error {
	if _, err := j.db.ExecContext(ctx,
		`DELETE FROM turns_inflight WHERE transport = $1 AND sender = $2`, transport, sender); err != nil {
		return fmt.Errorf("postgres: turn journal end: %w", err)
	}
	return nil
}

// TakeInterrupted returns and clears transport's turns that began but
// never ended, i.e. were cut off by a restart or crash. Call it once at
// startup, before new turns begin.
func (j *TurnJournal) TakeInterrupted(ctx context.Context, transport string) ([]InterruptedTurn, error) {
	tx, err := j.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("postgres: turn journal: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	rows, err := tx.QueryContext(ctx,
		`SELECT sender, preview, started_at FROM turns_inflight WHERE transport = $1 ORDER BY started_at`, transport)
	if err != nil {
		return nil, fmt.Errorf("postgres: turn journal list: %w", err)
	}
	var out []InterruptedTurn
	for rows.Next() {
		var it InterruptedTurn
		if err := rows.Scan(&it.Sender, &it.Preview, &it.StartedAt); err != nil {
			rows.Close() //nolint:errcheck,gosec // primary error is returned
			return nil, fmt.Errorf("postgres: turn journal scan: %w", err)
		}
		out = append(out, it)
	}
	rows.Close() //nolint:errcheck,gosec // iteration finished
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: turn journal: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM turns_inflight WHERE transport = $1`, transport); err != nil {
		return nil, fmt.Errorf("postgres: turn journal clear: %w", err)
	}
	return out, tx.Commit()
}
