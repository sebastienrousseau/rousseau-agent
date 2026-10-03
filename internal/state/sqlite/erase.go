package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/sebastienrousseau/rousseau-agent/internal/senderkey"
)

// EraseReport says what an erasure removed.
type EraseReport struct {
	// SessionIDs are the sessions erased. Callers holding
	// provider-side copies (claude transcripts) erase those too.
	SessionIDs []string
	// Rows counts deleted rows per table.
	Rows map[string]int64
}

// perSessionTables hold rows keyed by session_id.
var perSessionTables = []string{"session_messages", "session_costs", "claude_sessions", "recall_vectors", "reliability_samples"}

// EraseSender removes everything this store holds for sender (GDPR
// Article 17): its sessions, their messages and FTS rows, its jid mapping and
// identity handles, SSO bindings, cron jobs delivering to it, and the
// per-session rows in session_costs, claude_sessions, recall_vectors
// and reliability_samples. Tables a deployment never created are
// skipped. Idempotent.
//
// Sessions saved before sender tracking (empty sender) cannot be
// attributed and are not touched.
func (s *Store) EraseSender(ctx context.Context, sender string) (EraseReport, error) {
	if strings.TrimSpace(sender) == "" {
		return EraseReport{Rows: map[string]int64{}}, fmt.Errorf("sqlite: erase: empty sender")
	}
	steps := []senderStep{
		{"jid_sessions", `DELETE FROM jid_sessions WHERE jid = ?`, []any{sender}},
		{"identity_handles", `DELETE FROM identity_handles WHERE sender = ?`, []any{sender}},
		{"sso_bindings", `DELETE FROM sso_bindings WHERE external_id = ?`, []any{sender}},
		{"cron_jobs", `DELETE FROM cron_jobs WHERE deliver_to = ?`, []any{sender}},
	}
	// A namespaced key ("signal:+44...") scopes the transport-keyed
	// tables to that transport; cron targets are bare addresses.
	if t, bare, ok := senderkey.Split(sender); ok {
		steps[1] = senderStep{"identity_handles", `DELETE FROM identity_handles WHERE transport = ? AND sender = ?`, []any{t, bare}}
		steps[2] = senderStep{"sso_bindings", `DELETE FROM sso_bindings WHERE transport = ? AND external_id = ?`, []any{t, bare}}
		steps[3] = senderStep{"cron_jobs", `DELETE FROM cron_jobs WHERE deliver_to = ?`, []any{bare}}
	}
	return s.erase(ctx, `SELECT id FROM sessions WHERE sender = ?`, []any{sender}, steps)
}

// EraseIdleSessions removes sessions not updated since cutoff, with
// their per-session rows, and drops jid mappings that pointed at them
// (that sender's next message starts a fresh session). It implements
// state.session_ttl retention.
func (s *Store) EraseIdleSessions(ctx context.Context, cutoff time.Time) (EraseReport, error) {
	return s.erase(ctx,
		`SELECT id FROM sessions WHERE updated_at < ?`, []any{cutoff.UTC().Format("2006-01-02T15:04:05.000Z")},
		nil)
}

type senderStep struct {
	table, q string
	args     []any
}

// erase deletes the sessions selectQuery returns plus their
// per-session rows and jid mappings, then any sender-level steps, in
// one transaction on one connection with secure_delete on (freed pages
// are zeroed). It then optimises the FTS index and truncates the WAL
// so deleted text does not linger in either.
func (s *Store) erase(ctx context.Context, selectQuery string, selectArgs []any, steps []senderStep) (EraseReport, error) {
	rep := EraseReport{Rows: map[string]int64{}}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return rep, fmt.Errorf("sqlite: erase: conn: %w", err)
	}
	defer conn.Close() //nolint:errcheck // returns the connection to the pool

	if _, err := conn.ExecContext(ctx, `PRAGMA secure_delete = ON`); err != nil {
		return rep, fmt.Errorf("sqlite: erase: secure_delete: %w", err)
	}
	defer conn.ExecContext(context.Background(), `PRAGMA secure_delete = OFF`) //nolint:errcheck // restore pooled connection default

	if rep.SessionIDs, err = queryStrings(ctx, conn, selectQuery, selectArgs...); err != nil {
		return rep, fmt.Errorf("sqlite: erase: list sessions: %w", err)
	}
	tables, err := queryStrings(ctx, conn, `SELECT name FROM sqlite_master WHERE type = 'table'`)
	if err != nil {
		return rep, fmt.Errorf("sqlite: erase: tables: %w", err)
	}
	existing := map[string]bool{}
	for _, t := range tables {
		existing[t] = true
	}

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
		for _, table := range perSessionTables {
			if err := exec(table, `DELETE FROM `+table+` WHERE session_id = ?`, id); err != nil { //nolint:gosec // table names are package constants
				return rep, err
			}
		}
		if err := exec("jid_sessions", `DELETE FROM jid_sessions WHERE session_id = ?`, id); err != nil {
			return rep, err
		}
		if err := exec("sessions", `DELETE FROM sessions WHERE id = ?`, id); err != nil {
			return rep, err
		}
	}
	for _, st := range steps {
		if err := exec(st.table, st.q, st.args...); err != nil {
			return rep, err
		}
	}
	if err := tx.Commit(); err != nil {
		return rep, fmt.Errorf("sqlite: erase: commit: %w", err)
	}
	if len(rep.SessionIDs) == 0 && len(steps) == 0 {
		return rep, nil
	}
	// Deleted FTS terms can survive in index segments until merged.
	for _, fts := range []string{"messages_fts", "titles_fts"} {
		if !existing[fts] {
			continue
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO `+fts+`(`+fts+`) VALUES('optimize')`); err != nil { //nolint:gosec // fixed table names
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

func queryStrings(ctx context.Context, conn *sql.Conn, q string, args ...any) ([]string, error) {
	rows, err := conn.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // read-only iteration
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// SenderKeys returns the stored sender keys that name sender: the key
// itself, or any "<transport>:<sender>" key whose bare part is sender.
// It lets an operator erase by the bare identifier when only one
// transport holds it, and shows the choices when several do.
func (s *Store) SenderKeys(ctx context.Context, sender string) ([]string, error) {
	const q = `
SELECT sender AS k FROM sessions WHERE sender = ?1 OR substr(sender, instr(sender, ':') + 1) = ?1
UNION
SELECT jid AS k FROM jid_sessions WHERE jid = ?1 OR substr(jid, instr(jid, ':') + 1) = ?1`
	rows, err := s.db.QueryContext(ctx, q, sender)
	if err != nil && isNoSuchTable(err) {
		rows, err = s.db.QueryContext(ctx,
			`SELECT DISTINCT sender FROM sessions WHERE sender = ?1 OR substr(sender, instr(sender, ':') + 1) = ?1`, sender)
	}
	if err != nil {
		return nil, fmt.Errorf("sqlite: sender keys: %w", err)
	}
	defer rows.Close() //nolint:errcheck // read-only iteration
	var out []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, fmt.Errorf("sqlite: sender keys: %w", err)
		}
		if k == sender || senderkey.Bare(k) == sender {
			out = append(out, k)
		}
	}
	return out, rows.Err()
}
