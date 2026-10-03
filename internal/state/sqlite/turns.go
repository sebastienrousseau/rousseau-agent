package sqlite

import (
	"context"
	"fmt"
	"strings"
	"time"
)

const turnJournalSchema = `
CREATE TABLE IF NOT EXISTS turns_inflight (
    transport  TEXT NOT NULL,
    sender     TEXT NOT NULL,
    preview    TEXT NOT NULL,
    started_at TEXT NOT NULL,
    PRIMARY KEY (transport, sender)
);`

// TurnJournal records agent turns in flight so a daemon that restarts
// mid-turn can tell the sender their message was interrupted, instead
// of the reply silently never arriving.
type TurnJournal struct{ s *Store }

// InterruptedTurn is a turn that started but never ended.
type InterruptedTurn struct {
	Sender    string
	Preview   string
	StartedAt time.Time
}

// NewTurnJournal installs the journal table.
func NewTurnJournal(ctx context.Context, s *Store) (*TurnJournal, error) {
	if _, err := s.db.ExecContext(ctx, turnJournalSchema); err != nil {
		return nil, fmt.Errorf("sqlite: turn journal: %w", err)
	}
	return &TurnJournal{s: s}, nil
}

// previewRunes bounds the stored start of the message.
const previewRunes = 120

// Begin records that sender's turn on transport has started.
func (j *TurnJournal) Begin(ctx context.Context, transport, sender, body string) error {
	preview := strings.Join(strings.Fields(body), " ")
	if r := []rune(preview); len(r) > previewRunes {
		preview = string(r[:previewRunes]) + "…"
	}
	_, err := j.s.db.ExecContext(ctx, `
INSERT INTO turns_inflight (transport, sender, preview, started_at) VALUES (?, ?, ?, ?)
ON CONFLICT(transport, sender) DO UPDATE SET preview = excluded.preview, started_at = excluded.started_at`,
		transport, sender, preview, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return fmt.Errorf("sqlite: turn journal begin: %w", err)
	}
	return nil
}

// End records that sender's turn finished (reply sent or failed).
func (j *TurnJournal) End(ctx context.Context, transport, sender string) error {
	if _, err := j.s.db.ExecContext(ctx,
		`DELETE FROM turns_inflight WHERE transport = ? AND sender = ?`, transport, sender); err != nil {
		return fmt.Errorf("sqlite: turn journal end: %w", err)
	}
	return nil
}

// TakeInterrupted returns and clears transport's turns that began but
// never ended, i.e. were cut off by a restart or crash. Call it once at
// startup, before new turns begin.
func (j *TurnJournal) TakeInterrupted(ctx context.Context, transport string) ([]InterruptedTurn, error) {
	tx, err := j.s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("sqlite: turn journal: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	rows, err := tx.QueryContext(ctx,
		`SELECT sender, preview, started_at FROM turns_inflight WHERE transport = ? ORDER BY started_at`, transport)
	if err != nil {
		return nil, fmt.Errorf("sqlite: turn journal list: %w", err)
	}
	var out []InterruptedTurn
	for rows.Next() {
		var it InterruptedTurn
		var at string
		if err := rows.Scan(&it.Sender, &it.Preview, &at); err != nil {
			rows.Close() //nolint:errcheck,gosec // primary error is returned
			return nil, fmt.Errorf("sqlite: turn journal scan: %w", err)
		}
		it.StartedAt, _ = time.Parse(time.RFC3339, at) //nolint:errcheck // zero time on a malformed row is harmless
		out = append(out, it)
	}
	rows.Close() //nolint:errcheck,gosec // iteration finished
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite: turn journal: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM turns_inflight WHERE transport = ?`, transport); err != nil {
		return nil, fmt.Errorf("sqlite: turn journal clear: %w", err)
	}
	return out, tx.Commit()
}
