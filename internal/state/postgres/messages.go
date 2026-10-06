package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/sebastienrousseau/rousseau-agent/internal/model"
	"github.com/sebastienrousseau/rousseau-agent/internal/state"
	"github.com/sebastienrousseau/rousseau-agent/internal/state/history"
)

// Schema version 2 mirrors the SQLite store: each message is its own
// row, written once, and sessions holds the metadata plus the view
// (head, base_seq, next_seq, view_hash). See package history.
const schemaVersion = 2

// versionTableDDL records the schema version (Postgres has no
// PRAGMA user_version). A store with sessions and no row is version 1.
const versionTableDDL = `
CREATE TABLE IF NOT EXISTS rousseau_schema (
    id      INTEGER PRIMARY KEY CHECK (id = 1),
    version INTEGER NOT NULL
);`

const v2Schema = `
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS head TEXT NOT NULL DEFAULT '[]';
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS base_seq BIGINT NOT NULL DEFAULT 0;
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS next_seq BIGINT NOT NULL DEFAULT 0;
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS view_hash TEXT NOT NULL DEFAULT '';

DROP INDEX IF EXISTS idx_sessions_search_vector;
ALTER TABLE sessions DROP COLUMN IF EXISTS search_vector;
-- Weight A: a title match outranks a message match (weight D).
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS title_vector tsvector
    GENERATED ALWAYS AS (setweight(to_tsvector('english', coalesce(title, '')), 'A')) STORED;
CREATE INDEX IF NOT EXISTS idx_sessions_title_vector ON sessions USING GIN (title_vector);

CREATE TABLE IF NOT EXISTS session_messages (
    session_id  TEXT        NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    seq         BIGINT      NOT NULL,
    message     TEXT        NOT NULL,
    hash        TEXT        NOT NULL,
    body        TEXT        NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL,
    body_vector tsvector GENERATED ALWAYS AS (to_tsvector('english', body)) STORED,
    PRIMARY KEY (session_id, seq)
);
CREATE INDEX IF NOT EXISTS idx_session_messages_body_vector ON session_messages USING GIN (body_vector);
`

// ErrNeedsMigration is returned by Open for an older store that holds
// sessions; `rousseau migrate` upgrades it.
var ErrNeedsMigration = errors.New("postgres: session store needs `rousseau migrate`")

type querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func readVersion(ctx context.Context, db querier) (int, error) {
	if _, err := db.ExecContext(ctx, versionTableDDL); err != nil {
		return 0, fmt.Errorf("postgres: version table: %w", err)
	}
	var v int
	err := db.QueryRowContext(ctx, `SELECT version FROM rousseau_schema WHERE id = 1`).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return 1, nil
	}
	if err != nil {
		return 0, fmt.Errorf("postgres: read version: %w", err)
	}
	return v, nil
}

func setVersion(ctx context.Context, db querier, v int) error {
	if _, err := db.ExecContext(ctx, `
INSERT INTO rousseau_schema (id, version) VALUES (1, $1)
ON CONFLICT (id) DO UPDATE SET version = EXCLUDED.version`, v); err != nil {
		return fmt.Errorf("postgres: set version: %w", err)
	}
	return nil
}

// ensureVersion mirrors the SQLite store: an empty older store is
// upgraded in place, a current one has its idempotent schema
// reapplied, and an older one holding sessions is refused.
func (s *Store) ensureVersion(ctx context.Context) error {
	v, err := readVersion(ctx, s.db)
	if err != nil {
		return err
	}
	switch {
	case v == schemaVersion:
		if _, err := s.db.ExecContext(ctx, v2Schema); err != nil {
			return fmt.Errorf("postgres: apply v2 schema: %w", err)
		}
		return nil
	case v > schemaVersion:
		return fmt.Errorf("postgres: store schema version %d is newer than this build supports (%d)", v, schemaVersion)
	}
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions`).Scan(&n); err != nil {
		return fmt.Errorf("postgres: count sessions: %w", err)
	}
	if n > 0 {
		return fmt.Errorf("%w: schema version %d with %d sessions (run `rousseau migrate --dry-run`, then `rousseau migrate`)",
			ErrNeedsMigration, v, n)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("postgres: upgrade empty store: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	if _, err := tx.ExecContext(ctx, v2Schema); err != nil {
		return fmt.Errorf("postgres: apply v2 schema: %w", err)
	}
	if err := setVersion(ctx, tx, schemaVersion); err != nil {
		return err
	}
	return tx.Commit()
}

// Save writes a Session, appending only the messages after the stored
// view; a rewritten view keeps every stored row (see history.Align).
// Concurrent saves of one session serialise on an advisory lock.
func (s *Store) Save(ctx context.Context, sess *model.Session) error {
	meta := *sess
	meta.Messages = nil
	payload, err := json.Marshal(&meta)
	if err != nil {
		return fmt.Errorf("postgres: marshal session: %w", err)
	}
	msgs, err := history.EncodeAll(sess.Messages)
	if err != nil {
		return fmt.Errorf("postgres: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("postgres: save session: begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, sess.ID); err != nil {
		return fmt.Errorf("postgres: save session: lock: %w", err)
	}

	var (
		exists            = true
		count, base, next int64
		storedHash        string
		headJSON          = "[]"
		appendFrom        int
		rewriteHead       bool
	)
	err = tx.QueryRowContext(ctx,
		`SELECT message_count, base_seq, next_seq, view_hash FROM sessions WHERE id = $1`, sess.ID).
		Scan(&count, &base, &next, &storedHash)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		exists = false
	case err != nil:
		return fmt.Errorf("postgres: save session: read view: %w", err)
	}
	switch {
	case !exists:
	case history.Grew(msgs, int(count), storedHash):
		appendFrom = int(count)
	default:
		seqs, hashes, err := storedHashes(ctx, tx, sess.ID)
		if err != nil {
			return err
		}
		a := history.Align(hashes, msgs)
		base = next
		if a.Base < len(seqs) {
			base = seqs[a.Base]
		}
		headJSON, appendFrom, rewriteHead = history.HeadJSON(msgs, a), a.AppendFrom, true
	}

	first := next
	next += int64(len(msgs) - appendFrom)
	viewHash := history.ViewHash(msgs, len(msgs))
	if !exists {
		_, err = tx.ExecContext(ctx, `
INSERT INTO sessions (id, title, payload, message_count, created_at, updated_at, sender, head, base_seq, next_seq, view_hash)
VALUES ($1, $2, $3, $4, $5, $6, $7, '[]', 0, $8, $9)`,
			sess.ID, sess.Title, string(payload), len(msgs), sess.CreatedAt.UTC(), sess.UpdatedAt.UTC(), sess.Sender, next, viewHash)
	} else {
		_, err = tx.ExecContext(ctx, `
UPDATE sessions SET title = $1, payload = $2, message_count = $3, updated_at = $4, sender = $5,
       base_seq = $6, next_seq = $7, view_hash = $8,
       head = CASE WHEN $9 THEN $10 ELSE head END
WHERE id = $11`,
			sess.Title, string(payload), len(msgs), sess.UpdatedAt.UTC(), sess.Sender,
			base, next, viewHash, rewriteHead, headJSON, sess.ID)
	}
	if err != nil {
		return fmt.Errorf("postgres: save session: %w", err)
	}
	for i := appendFrom; i < len(msgs); i++ {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO session_messages (session_id, seq, message, hash, body, created_at) VALUES ($1, $2, $3, $4, $5, $6)`,
			sess.ID, first, string(msgs[i].JSON), msgs[i].Hash, msgs[i].Body, sess.Messages[i].CreatedAt.UTC()); err != nil {
			return fmt.Errorf("postgres: save session: append: %w", err)
		}
		first++
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("postgres: save session: commit: %w", err)
	}
	return nil
}

func storedHashes(ctx context.Context, db querier, id string) ([]int64, []string, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT seq, hash FROM session_messages WHERE session_id = $1 ORDER BY seq`, id)
	if err != nil {
		return nil, nil, fmt.Errorf("postgres: save session: read rows: %w", err)
	}
	defer rows.Close() //nolint:errcheck // read-only iteration
	var seqs []int64
	var hashes []string
	for rows.Next() {
		var seq int64
		var h string
		if err := rows.Scan(&seq, &h); err != nil {
			return nil, nil, fmt.Errorf("postgres: save session: scan rows: %w", err)
		}
		seqs, hashes = append(seqs, seq), append(hashes, h)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("postgres: save session: rows: %w", err)
	}
	return seqs, hashes, nil
}

// Load returns the Session identified by id, or state.ErrNotFound.
func (s *Store) Load(ctx context.Context, id string) (*model.Session, error) {
	return loadSession(ctx, s.db, id)
}

func loadSession(ctx context.Context, db querier, id string) (*model.Session, error) {
	var payload, headJSON, sender string
	var base int64
	err := db.QueryRowContext(ctx,
		`SELECT payload, head, base_seq, sender FROM sessions WHERE id = $1`, id).Scan(&payload, &headJSON, &base, &sender)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, state.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("postgres: load session: %w", err)
	}
	sess := &model.Session{}
	if err := json.Unmarshal([]byte(payload), sess); err != nil {
		return nil, fmt.Errorf("postgres: unmarshal session: %w", err)
	}
	var msgs []model.Message
	if err := json.Unmarshal([]byte(headJSON), &msgs); err != nil {
		return nil, fmt.Errorf("postgres: unmarshal head: %w", err)
	}
	rows, err := db.QueryContext(ctx,
		`SELECT message FROM session_messages WHERE session_id = $1 AND seq >= $2 ORDER BY seq`, id, base)
	if err != nil {
		return nil, fmt.Errorf("postgres: load messages: %w", err)
	}
	defer rows.Close() //nolint:errcheck // read-only iteration
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, fmt.Errorf("postgres: scan message: %w", err)
		}
		var m model.Message
		if err := json.Unmarshal([]byte(raw), &m); err != nil {
			return nil, fmt.Errorf("postgres: unmarshal message: %w", err)
		}
		msgs = append(msgs, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: load messages: %w", err)
	}
	if len(msgs) == 0 {
		msgs = nil // as NewSession leaves it and v1 payloads stored it ("messages":null)
	}
	sess.Messages = msgs
	// The column is authoritative: migration rewrites sender keys
	// there, not inside each payload.
	sess.Sender = sender
	return sess, nil
}
