package sqlite

import (
	"context"
	"fmt"
	"strings"
)

// EraseReport says what EraseSender removed.
type EraseReport struct {
	// SessionIDs are the sessions that belonged to the sender. Callers
	// holding provider-side copies (claude transcripts) erase those too.
	SessionIDs []string
	// Rows counts deleted rows per table.
	Rows map[string]int64
}

// EraseSender removes everything this store holds for sender (GDPR
// Article 17): its sessions (and their FTS rows), its jid mapping and
// identity handles, SSO bindings, cron jobs delivering to it, and the
// per-session rows in session_costs, claude_sessions, recall_vectors
// and reliability_samples. Tables a deployment never created are
// skipped. It runs on one connection with secure_delete on, so freed
// pages are zeroed, then optimises the FTS index and checkpoints the
// WAL so deleted text does not linger in either. Idempotent.
//
// Sessions saved before sender tracking (empty sender) cannot be
// attributed and are not touched.
func (s *Store) EraseSender(ctx context.Context, sender string) (EraseReport, error) {
	rep := EraseReport{Rows: map[string]int64{}}
	if strings.TrimSpace(sender) == "" {
		return rep, fmt.Errorf("sqlite: erase: empty sender")
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return rep, fmt.Errorf("sqlite: erase: conn: %w", err)
	}
	defer conn.Close() //nolint:errcheck // returns the connection to the pool

	if _, err := conn.ExecContext(ctx, `PRAGMA secure_delete = ON`); err != nil {
		return rep, fmt.Errorf("sqlite: erase: secure_delete: %w", err)
	}
	defer conn.ExecContext(context.Background(), `PRAGMA secure_delete = OFF`) //nolint:errcheck // restore pooled connection default

	rows, err := conn.QueryContext(ctx, `SELECT id FROM sessions WHERE sender = ?`, sender)
	if err != nil {
		return rep, fmt.Errorf("sqlite: erase: list sessions: %w", err)
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close() //nolint:errcheck,gosec // primary error is returned
			return rep, fmt.Errorf("sqlite: erase: scan: %w", err)
		}
		rep.SessionIDs = append(rep.SessionIDs, id)
	}
	rows.Close() //nolint:errcheck,gosec // iteration finished
	if err := rows.Err(); err != nil {
		return rep, fmt.Errorf("sqlite: erase: list sessions: %w", err)
	}

	existing := map[string]bool{}
	trows, err := conn.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type IN ('table')`)
	if err != nil {
		return rep, fmt.Errorf("sqlite: erase: tables: %w", err)
	}
	for trows.Next() {
		var n string
		if trows.Scan(&n) == nil {
			existing[n] = true
		}
	}
	trows.Close() //nolint:errcheck,gosec // iteration finished

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return rep, fmt.Errorf("sqlite: erase: begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit

	exec := func(table, q string, args ...any) error {
		if !existing[table] {
			return nil
		}
		res, err := tx.ExecContext(ctx, q, args...)
		if err != nil {
			return fmt.Errorf("sqlite: erase %s: %w", table, err)
		}
		n, _ := res.RowsAffected() //nolint:errcheck // driver always reports it
		rep.Rows[table] += n
		return nil
	}
	for _, id := range rep.SessionIDs {
		for _, table := range []string{"session_costs", "claude_sessions", "recall_vectors", "reliability_samples"} {
			if err := exec(table, `DELETE FROM `+table+` WHERE session_id = ?`, id); err != nil { //nolint:gosec // table names are constants above
				return rep, err
			}
		}
	}
	steps := []struct{ table, q string }{
		{"sessions", `DELETE FROM sessions WHERE sender = ?`},
		{"jid_sessions", `DELETE FROM jid_sessions WHERE jid = ?`},
		{"identity_handles", `DELETE FROM identity_handles WHERE sender = ?`},
		{"sso_bindings", `DELETE FROM sso_bindings WHERE external_id = ?`},
		{"cron_jobs", `DELETE FROM cron_jobs WHERE deliver_to = ?`},
	}
	for _, st := range steps {
		if err := exec(st.table, st.q, sender); err != nil {
			return rep, err
		}
	}
	if err := tx.Commit(); err != nil {
		return rep, fmt.Errorf("sqlite: erase: commit: %w", err)
	}
	// Deleted FTS terms can survive in index segments until merged.
	if existing["sessions_fts"] {
		if _, err := conn.ExecContext(ctx, `INSERT INTO sessions_fts(sessions_fts) VALUES('optimize')`); err != nil {
			return rep, fmt.Errorf("sqlite: erase: fts optimize: %w", err)
		}
	}
	// Move the WAL into the main file (where secure_delete zeroed the
	// freed pages) and truncate it, so deleted rows are not left in it.
	if _, err := conn.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		return rep, fmt.Errorf("sqlite: erase: checkpoint: %w", err)
	}
	return rep, nil
}
