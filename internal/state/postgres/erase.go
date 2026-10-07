package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/sebastienrousseau/rousseau-agent/internal/senderkey"
	sqlitestore "github.com/sebastienrousseau/rousseau-agent/internal/state/sqlite"
)

// EraseReport mirrors the SQLite report so the CLI's erasure and
// retention interfaces are satisfied by either driver.
type EraseReport = sqlitestore.EraseReport

// perSessionTables hold rows keyed by session_id.
var perSessionTables = []string{"session_messages", "session_costs", "claude_sessions", "reliability_samples"}

// EraseSender removes everything this store holds for sender (GDPR
// Article 17): its sessions and their messages, its jid mapping and
// identity handles, SSO bindings, cron jobs delivering to it, and the
// per-session rows in session_costs, claude_sessions and
// reliability_samples. Tables a deployment never created are skipped.
// Idempotent.
//
// Postgres has no secure_delete: the deleted tuples are reclaimed by
// autovacuum, and an operator who must guarantee the bytes are gone
// from disk runs VACUUM FULL on the affected tables afterwards (it
// cannot run inside this transaction). WAL segments are recycled by
// the server's normal checkpointing.
//
// Sessions saved before sender tracking (empty sender) cannot be
// attributed and are not touched.
func (s *Store) EraseSender(ctx context.Context, sender string) (EraseReport, error) {
	if strings.TrimSpace(sender) == "" {
		return EraseReport{Rows: map[string]int64{}}, fmt.Errorf("postgres: erase: empty sender")
	}
	steps := []senderStep{
		{"jid_sessions", `DELETE FROM jid_sessions WHERE jid = $1`, []any{sender}},
		{"identity_handles", `DELETE FROM identity_handles WHERE sender = $1`, []any{sender}},
		{"sso_bindings", `DELETE FROM sso_bindings WHERE external_id = $1`, []any{sender}},
		{"cron_jobs", `DELETE FROM cron_jobs WHERE deliver_to = $1`, []any{sender}},
	}
	// A namespaced key ("signal:+44...") scopes the transport-keyed
	// tables to that transport; cron targets are bare addresses.
	if t, bare, ok := senderkey.Split(sender); ok {
		steps[1] = senderStep{"identity_handles", `DELETE FROM identity_handles WHERE transport = $1 AND sender = $2`, []any{t, bare}}
		steps[2] = senderStep{"sso_bindings", `DELETE FROM sso_bindings WHERE transport = $1 AND external_id = $2`, []any{t, bare}}
		steps[3] = senderStep{"cron_jobs", `DELETE FROM cron_jobs WHERE deliver_to = $1`, []any{bare}}
	}
	return s.erase(ctx, `SELECT id FROM sessions WHERE sender = $1`, []any{sender}, steps)
}

// EraseIdleSessions removes sessions not updated since cutoff, with
// their per-session rows, and drops jid mappings that pointed at them
// (that sender's next message starts a fresh session). It implements
// state.session_ttl retention.
func (s *Store) EraseIdleSessions(ctx context.Context, cutoff time.Time) (EraseReport, error) {
	return s.erase(ctx, `SELECT id FROM sessions WHERE updated_at < $1`, []any{cutoff.UTC()}, nil)
}

type senderStep struct {
	table, q string
	args     []any
}

// erase deletes the sessions selectQuery returns plus their
// per-session rows and jid mappings, then any sender-level steps, in
// one transaction.
func (s *Store) erase(ctx context.Context, selectQuery string, selectArgs []any, steps []senderStep) (EraseReport, error) {
	rep := EraseReport{Rows: map[string]int64{}}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return rep, fmt.Errorf("postgres: erase: begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit

	if rep.SessionIDs, err = queryStrings(ctx, tx, selectQuery, selectArgs...); err != nil {
		return rep, fmt.Errorf("postgres: erase: list sessions: %w", err)
	}
	existing, err := existingTables(ctx, tx)
	if err != nil {
		return rep, fmt.Errorf("postgres: erase: tables: %w", err)
	}
	e := eraser{tx: tx, existing: existing, rep: &rep}
	for _, id := range rep.SessionIDs {
		if err := e.session(ctx, id); err != nil {
			return rep, err
		}
	}
	for _, st := range steps {
		if err := e.exec(ctx, st.table, st.q, st.args...); err != nil {
			return rep, err
		}
	}
	if err := tx.Commit(); err != nil {
		return rep, fmt.Errorf("postgres: erase: commit: %w", err)
	}
	return rep, nil
}

// eraser runs deletes inside one erasure transaction, skipping tables
// the deployment never created and counting rows per table.
type eraser struct {
	tx       *sql.Tx
	existing map[string]bool
	rep      *EraseReport
}

// session deletes one session with its per-session rows and the jid
// mappings that point at it.
func (e eraser) session(ctx context.Context, id string) error {
	for _, table := range perSessionTables {
		if err := e.exec(ctx, table, `DELETE FROM `+table+` WHERE session_id = $1`, id); err != nil { //nolint:gosec // table names are package constants
			return err
		}
	}
	if err := e.exec(ctx, "jid_sessions", `DELETE FROM jid_sessions WHERE session_id = $1`, id); err != nil {
		return err
	}
	return e.exec(ctx, "sessions", `DELETE FROM sessions WHERE id = $1`, id)
}

func (e eraser) exec(ctx context.Context, table, q string, args ...any) error {
	if !e.existing[table] {
		return nil
	}
	res, err := e.tx.ExecContext(ctx, q, args...)
	if err != nil {
		return fmt.Errorf("postgres: erase %s: %w", table, err)
	}
	n, _ := res.RowsAffected() //nolint:errcheck // driver always reports it
	e.rep.Rows[table] += n
	return nil
}

// existingTables lists the tables in the connection's current schema
// so erasure can skip tables a deployment never created.
func existingTables(ctx context.Context, q querier) (map[string]bool, error) {
	names, err := queryStrings(ctx, q, `SELECT table_name FROM information_schema.tables WHERE table_schema = current_schema()`)
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(names))
	for _, n := range names {
		out[n] = true
	}
	return out, nil
}

// SenderKeys returns the stored sender keys that name sender: the key
// itself, or any "<transport>:<sender>" key whose bare part is sender.
// It lets an operator erase by the bare identifier when only one
// transport holds it, and shows the choices when several do.
func (s *Store) SenderKeys(ctx context.Context, sender string) ([]string, error) {
	existing, err := existingTables(ctx, s.db)
	if err != nil {
		return nil, fmt.Errorf("postgres: sender keys: %w", err)
	}
	q := `SELECT DISTINCT sender AS k FROM sessions WHERE sender = $1 OR substr(sender, position(':' in sender) + 1) = $1`
	if existing["jid_sessions"] {
		q += ` UNION SELECT jid AS k FROM jid_sessions WHERE jid = $1 OR substr(jid, position(':' in jid) + 1) = $1`
	}
	keys, err := queryStrings(ctx, s.db, q, sender)
	if err != nil {
		return nil, fmt.Errorf("postgres: sender keys: %w", err)
	}
	var out []string
	for _, k := range keys {
		if k == sender || senderkey.Bare(k) == sender {
			out = append(out, k)
		}
	}
	return out, nil
}
