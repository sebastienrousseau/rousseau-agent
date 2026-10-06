package sqlite

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

// Schema version 2 stores each message as its own row, written once
// (append-only), instead of rewriting the whole conversation as one
// JSON payload on every turn. sessions keeps the metadata plus the
// "view" of the conversation the agent sees:
//
//   - head: messages that are not rows, as a JSON array. The
//     compressor's summary lands here when it folds older messages.
//   - base_seq: the first row in the view. Rows below it were folded
//     into head; they stay stored until the session is deleted.
//   - next_seq: the seq the next appended row gets.
//   - view_hash: the hash of the view's last message, so Save can tell
//     an append (the common case) from a rewrite in O(1).
//
// message_count is the view's length (head + rows from base_seq).
const schemaVersion = 2

const v2Schema = `
CREATE TABLE IF NOT EXISTS session_messages (
    session_id  TEXT    NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    seq         INTEGER NOT NULL,
    message     TEXT    NOT NULL,
    hash        TEXT    NOT NULL,
    body        TEXT    NOT NULL DEFAULT '',
    created_at  TEXT    NOT NULL,
    PRIMARY KEY (session_id, seq)
);

CREATE VIRTUAL TABLE IF NOT EXISTS messages_fts USING fts5(
    session_id UNINDEXED,
    seq UNINDEXED,
    body,
    tokenize = 'porter unicode61'
);

CREATE TRIGGER IF NOT EXISTS session_messages_fts_ai AFTER INSERT ON session_messages
WHEN NEW.body != '' BEGIN
    INSERT INTO messages_fts (session_id, seq, body) VALUES (NEW.session_id, NEW.seq, NEW.body);
END;

CREATE TRIGGER IF NOT EXISTS session_messages_fts_ad AFTER DELETE ON session_messages BEGIN
    DELETE FROM messages_fts WHERE session_id = OLD.session_id AND seq = OLD.seq;
END;

CREATE VIRTUAL TABLE IF NOT EXISTS titles_fts USING fts5(
    session_id UNINDEXED,
    title,
    tokenize = 'porter unicode61'
);

CREATE TRIGGER IF NOT EXISTS sessions_titles_ai AFTER INSERT ON sessions BEGIN
    INSERT INTO titles_fts (session_id, title) VALUES (NEW.id, NEW.title);
END;

CREATE TRIGGER IF NOT EXISTS sessions_titles_au AFTER UPDATE OF title ON sessions
WHEN OLD.title IS NOT NEW.title BEGIN
    DELETE FROM titles_fts WHERE session_id = OLD.id;
    INSERT INTO titles_fts (session_id, title) VALUES (NEW.id, NEW.title);
END;

CREATE TRIGGER IF NOT EXISTS sessions_titles_ad AFTER DELETE ON sessions BEGIN
    DELETE FROM titles_fts WHERE session_id = OLD.id;
    DELETE FROM session_messages WHERE session_id = OLD.id;
END;
`

// v2Columns are added to sessions by the v2 schema.
var v2Columns = []struct{ name, ddl string }{
	{"head", `ALTER TABLE sessions ADD COLUMN head TEXT NOT NULL DEFAULT '[]'`},
	{"base_seq", `ALTER TABLE sessions ADD COLUMN base_seq INTEGER NOT NULL DEFAULT 0`},
	{"next_seq", `ALTER TABLE sessions ADD COLUMN next_seq INTEGER NOT NULL DEFAULT 0`},
	{"view_hash", `ALTER TABLE sessions ADD COLUMN view_hash TEXT NOT NULL DEFAULT ''`},
}

// legacySearchDDL removes the v1 whole-session FTS index.
var legacySearchDDL = []string{
	`DROP TRIGGER IF EXISTS sessions_fts_ai`,
	`DROP TRIGGER IF EXISTS sessions_fts_au`,
	`DROP TRIGGER IF EXISTS sessions_fts_ad`,
	`DROP TABLE IF EXISTS sessions_fts`,
}

// execer is satisfied by *sql.DB, *sql.Conn and *sql.Tx.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// installV2 creates the v2 tables, columns and triggers and drops the
// v1 search index. Idempotent.
func installV2(ctx context.Context, db execer) error {
	for _, c := range v2Columns {
		if err := ensureColumnOn(ctx, db, c.name, c.ddl); err != nil {
			return err
		}
	}
	for _, q := range legacySearchDDL {
		if _, err := db.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("sqlite: drop legacy search: %w", err)
		}
	}
	if _, err := db.ExecContext(ctx, v2Schema); err != nil {
		return fmt.Errorf("sqlite: install v2 schema: %w", err)
	}
	return nil
}

// Save writes a Session. Messages already stored are not rewritten:
// Save appends the ones after the stored view. When the caller
// rewrote history (the compressor folding old messages into a
// summary), Save keeps every stored row, finds the longest run of
// stored rows the new view ends with, and records the rest of the
// view (the summary) as head. No stored message is ever overwritten.
func (s *Store) Save(ctx context.Context, sess *model.Session) error {
	meta := *sess
	meta.Messages = nil
	payload, err := json.Marshal(&meta)
	if err != nil {
		return fmt.Errorf("sqlite: marshal session: %w", err)
	}
	msgs, err := history.EncodeAll(sess.Messages)
	if err != nil {
		return fmt.Errorf("sqlite: %w", err)
	}

	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("sqlite: save session: conn: %w", err)
	}
	defer conn.Close() //nolint:errcheck // returns the connection to the pool
	// IMMEDIATE takes the write lock up front, so two savers of one
	// session serialise instead of both reading the same view.
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return fmt.Errorf("sqlite: save session: begin: %w", err)
	}
	done := false
	defer func() {
		if !done {
			_, _ = conn.ExecContext(context.Background(), `ROLLBACK`) //nolint:errcheck // the primary error is returned
		}
	}()

	var (
		exists      = true
		count, base int64
		next        int64
		storedHash  string
		headJSON    = "[]"
		appendFrom  int
		rewriteHead bool
	)
	err = conn.QueryRowContext(ctx,
		`SELECT message_count, base_seq, next_seq, view_hash FROM sessions WHERE id = ?`, sess.ID).
		Scan(&count, &base, &next, &storedHash)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		exists = false
	case err != nil:
		return fmt.Errorf("sqlite: save session: read view: %w", err)
	}

	switch {
	case !exists:
		appendFrom = 0
	case history.Grew(msgs, int(count), storedHash):
		appendFrom = int(count) // the view grew: append the new messages
	default:
		seqs, hashes, err := storedHashes(ctx, conn, sess.ID)
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

	first := next // seq of the first appended row
	next += int64(len(msgs) - appendFrom)
	viewHash := history.ViewHash(msgs, len(msgs))
	created := sess.CreatedAt.UTC().Format("2006-01-02T15:04:05.000Z")
	updated := sess.UpdatedAt.UTC().Format("2006-01-02T15:04:05.000Z")
	if !exists {
		_, err = conn.ExecContext(ctx, `
INSERT INTO sessions (id, title, payload, message_count, created_at, updated_at, sender,
                      head, base_seq, next_seq, view_hash)
VALUES (?, ?, ?, ?, ?, ?, ?, '[]', 0, ?, ?)`,
			sess.ID, sess.Title, string(payload), len(msgs), created, updated, sess.Sender, next, viewHash)
	} else {
		q := `UPDATE sessions SET title = ?, payload = ?, message_count = ?, updated_at = ?, sender = ?,
       base_seq = ?, next_seq = ?, view_hash = ?`
		args := []any{sess.Title, string(payload), len(msgs), updated, sess.Sender, base, next, viewHash}
		if rewriteHead {
			q += `, head = ?`
			args = append(args, headJSON)
		}
		q += ` WHERE id = ?`
		_, err = conn.ExecContext(ctx, q, append(args, sess.ID)...)
	}
	if err != nil {
		return fmt.Errorf("sqlite: save session: %w", err)
	}
	// The session row exists now (the messages reference it).
	for i := appendFrom; i < len(msgs); i++ {
		if _, err := conn.ExecContext(ctx, `
INSERT INTO session_messages (session_id, seq, message, hash, body, created_at)
VALUES (?, ?, ?, ?, ?, ?)`,
			sess.ID, first, string(msgs[i].JSON), msgs[i].Hash, msgs[i].Body,
			sess.Messages[i].CreatedAt.UTC().Format("2006-01-02T15:04:05.000Z")); err != nil {
			return fmt.Errorf("sqlite: save session: append: %w", err)
		}
		first++
	}

	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return fmt.Errorf("sqlite: save session: commit: %w", err)
	}
	done = true
	return nil
}

// storedHashes returns a session's row seqs and hashes in seq order.
func storedHashes(ctx context.Context, conn *sql.Conn, id string) ([]int64, []string, error) {
	rows, err := conn.QueryContext(ctx,
		`SELECT seq, hash FROM session_messages WHERE session_id = ? ORDER BY seq`, id)
	if err != nil {
		return nil, nil, fmt.Errorf("sqlite: save session: read rows: %w", err)
	}
	defer rows.Close() //nolint:errcheck // read-only iteration
	var seqs []int64
	var hashes []string
	for rows.Next() {
		var seq int64
		var h string
		if err := rows.Scan(&seq, &h); err != nil {
			return nil, nil, fmt.Errorf("sqlite: save session: scan rows: %w", err)
		}
		seqs, hashes = append(seqs, seq), append(hashes, h)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("sqlite: save session: rows: %w", err)
	}
	return seqs, hashes, nil
}

// Load returns the Session identified by id, or state.ErrNotFound.
func (s *Store) Load(ctx context.Context, id string) (*model.Session, error) {
	return loadSession(ctx, s.db, id)
}

func loadSession(ctx context.Context, db execer, id string) (*model.Session, error) {
	var payload, headJSON, sender string
	var base int64
	err := db.QueryRowContext(ctx,
		`SELECT payload, head, base_seq, sender FROM sessions WHERE id = ?`, id).Scan(&payload, &headJSON, &base, &sender)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, state.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("sqlite: load session: %w", err)
	}
	sess := &model.Session{}
	if err := json.Unmarshal([]byte(payload), sess); err != nil {
		return nil, fmt.Errorf("sqlite: unmarshal session: %w", err)
	}
	var msgs []model.Message
	if err := json.Unmarshal([]byte(headJSON), &msgs); err != nil {
		return nil, fmt.Errorf("sqlite: unmarshal head: %w", err)
	}
	rows, err := db.QueryContext(ctx,
		`SELECT message FROM session_messages WHERE session_id = ? AND seq >= ? ORDER BY seq`, id, base)
	if err != nil {
		return nil, fmt.Errorf("sqlite: load messages: %w", err)
	}
	defer rows.Close() //nolint:errcheck // read-only iteration
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, fmt.Errorf("sqlite: scan message: %w", err)
		}
		var m model.Message
		if err := json.Unmarshal([]byte(raw), &m); err != nil {
			return nil, fmt.Errorf("sqlite: unmarshal message: %w", err)
		}
		msgs = append(msgs, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite: load messages: %w", err)
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

// ensureColumnOn adds a column to sessions when it is missing.
func ensureColumnOn(ctx context.Context, db execer, name, ddl string) error {
	var n int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pragma_table_info('sessions') WHERE name = ?`, name).Scan(&n); err != nil {
		return fmt.Errorf("sqlite: probe column %s: %w", name, err)
	}
	if n > 0 {
		return nil
	}
	if _, err := db.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("sqlite: add column %s: %w", name, err)
	}
	return nil
}
