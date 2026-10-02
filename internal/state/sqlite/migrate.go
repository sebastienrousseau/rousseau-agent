package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
	"github.com/sebastienrousseau/rousseau-agent/internal/senderkey"
	"github.com/sebastienrousseau/rousseau-agent/internal/state/history"
)

// ErrNeedsMigration is returned by Open for a store written by an
// older version that holds sessions. `rousseau migrate` upgrades it;
// the daemon exits 78 on it so a supervisor does not restart-loop.
var ErrNeedsMigration = errors.New("sqlite: session store needs `rousseau migrate`")

// ErrAmbiguousSenders is returned by Migrate when a legacy sender key's
// transport cannot be told from its shape and no mapping was given.
var ErrAmbiguousSenders = errors.New("sqlite: migrate: ambiguous sender keys need --map")

func userVersion(ctx context.Context, db execer) (int, error) {
	var v int
	if err := db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&v); err != nil {
		return 0, fmt.Errorf("sqlite: read user_version: %w", err)
	}
	return v, nil
}

// ensureVersion brings a store opened by Open to the current schema:
// an empty older store is upgraded in place, a current one has its
// (idempotent) schema reapplied, and an older one holding sessions is
// refused with ErrNeedsMigration.
func (s *Store) ensureVersion(ctx context.Context) error {
	v, err := userVersion(ctx, s.db)
	if err != nil {
		return err
	}
	switch {
	case v == schemaVersion:
		return installV2(ctx, s.db)
	case v > schemaVersion:
		return fmt.Errorf("sqlite: store schema version %d is newer than this build supports (%d)", v, schemaVersion)
	}
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions`).Scan(&n); err != nil {
		return fmt.Errorf("sqlite: count sessions: %w", err)
	}
	if n > 0 {
		return fmt.Errorf("%w: schema version %d with %d sessions (run `rousseau migrate --dry-run`, then `rousseau migrate`)",
			ErrNeedsMigration, v, n)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sqlite: upgrade empty store: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	if err := installV2(ctx, tx); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`PRAGMA user_version = %d`, schemaVersion)); err != nil {
		return fmt.Errorf("sqlite: set user_version: %w", err)
	}
	return tx.Commit()
}

// MigrateOptions controls Migrate.
type MigrateOptions struct {
	// DryRun reports what would change and writes nothing.
	DryRun bool
	// Map assigns a transport to a legacy bare sender whose shape is
	// ambiguous (an E.164 number may be Signal or iMessage).
	Map map[string]string
	// Now stamps the backup file name; nil uses time.Now.
	Now func() time.Time
}

// KeyChange is one sender key rewritten by Migrate.
type KeyChange = senderkey.Change

// AmbiguousKey is a legacy sender whose transport Migrate cannot infer.
type AmbiguousKey = senderkey.Ambiguous

// MigrateReport describes a Migrate run.
type MigrateReport struct {
	FromVersion, ToVersion int
	Sessions, Messages     int
	Keys                   []KeyChange
	Ambiguous              []AmbiguousKey
	// Backup is the snapshot taken before migrating ("" on a dry run
	// or when nothing needed migrating).
	Backup string
}

// Migrate upgrades the store at path to the current schema: each
// session's messages become rows, and every sender key gains its
// transport prefix. It backs the store up first (VACUUM INTO, checked
// with integrity_check), runs in one transaction, and verifies every
// session loads identically through the new layout before it commits;
// any failure leaves the store as it was.
func Migrate(ctx context.Context, path string, opts MigrateOptions) (MigrateReport, error) {
	rep := MigrateReport{ToVersion: schemaVersion}
	db, err := sql.Open("sqlite", fileDSN(path))
	if err != nil {
		return rep, fmt.Errorf("sqlite: migrate: open: %w", err)
	}
	defer db.Close() //nolint:errcheck // nothing to report after the run
	ctx = context.WithoutCancel(ctx)

	if rep.FromVersion, err = userVersion(ctx, db); err != nil {
		return rep, err
	}
	if rep.FromVersion >= schemaVersion {
		rep.ToVersion = rep.FromVersion
		return rep, nil
	}
	var hasSessions int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'sessions'`).Scan(&hasSessions); err != nil {
		return rep, fmt.Errorf("sqlite: migrate: %w", err)
	}
	if hasSessions == 0 {
		return rep, errors.New("sqlite: migrate: no sessions table; nothing to migrate (Open creates a fresh store)")
	}

	keys, err := planKeys(ctx, db, opts.Map, &rep)
	if err != nil {
		return rep, err
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions`).Scan(&rep.Sessions); err != nil {
		return rep, fmt.Errorf("sqlite: migrate: %w", err)
	}
	if opts.DryRun {
		rep.Messages, err = countPayloadMessages(ctx, db)
		return rep, err
	}
	if len(rep.Ambiguous) > 0 {
		return rep, ErrAmbiguousSenders
	}

	now := time.Now
	if opts.Now != nil {
		now = opts.Now
	}
	if rep.Backup, err = backup(ctx, db, path, "pre-v2", now()); err != nil {
		return rep, err
	}

	conn, err := db.Conn(ctx)
	if err != nil {
		return rep, fmt.Errorf("sqlite: migrate: conn: %w", err)
	}
	defer conn.Close() //nolint:errcheck // returns the connection to the pool
	if _, err := conn.ExecContext(ctx, `BEGIN EXCLUSIVE`); err != nil {
		return rep, fmt.Errorf("sqlite: migrate: lock (is a daemon still running?): %w", err)
	}
	done := false
	defer func() {
		if !done {
			_, _ = conn.ExecContext(context.Background(), `ROLLBACK`) //nolint:errcheck // the primary error is returned
		}
	}()

	// Stores older than sender tracking lack these columns.
	for _, c := range []struct{ name, ddl string }{
		{"sender", `ALTER TABLE sessions ADD COLUMN sender TEXT NOT NULL DEFAULT ''`},
		{"search_text", `ALTER TABLE sessions ADD COLUMN search_text TEXT NOT NULL DEFAULT ''`},
	} {
		if err := ensureColumnOn(ctx, conn, c.name, c.ddl); err != nil {
			return rep, err
		}
	}
	if _, err := conn.ExecContext(ctx,
		`CREATE INDEX IF NOT EXISTS idx_sessions_sender_updated ON sessions(sender, updated_at DESC)`); err != nil {
		return rep, fmt.Errorf("sqlite: migrate: index sender: %w", err)
	}
	if err := installV2(ctx, conn); err != nil {
		return rep, err
	}
	originals, err := splitPayloads(ctx, conn, &rep)
	if err != nil {
		return rep, err
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO titles_fts (session_id, title) SELECT id, title FROM sessions`); err != nil {
		return rep, fmt.Errorf("sqlite: migrate: index titles: %w", err)
	}
	if err := rewriteKeys(ctx, conn, keys); err != nil {
		return rep, err
	}
	if err := verify(ctx, conn, originals); err != nil {
		return rep, err
	}
	if _, err := conn.ExecContext(ctx, fmt.Sprintf(`PRAGMA user_version = %d`, schemaVersion)); err != nil {
		return rep, fmt.Errorf("sqlite: migrate: set version: %w", err)
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return rep, fmt.Errorf("sqlite: migrate: commit: %w", err)
	}
	done = true
	return rep, nil
}

// planKeys maps every distinct legacy sender key to its namespaced
// form, recording ambiguous ones in rep.
func planKeys(ctx context.Context, db execer, mapping map[string]string, rep *MigrateReport) (map[string]string, error) {
	seen := map[string]bool{}
	var senders []string
	var haveSender int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pragma_table_info('sessions') WHERE name = 'sender'`).Scan(&haveSender); err != nil {
		return nil, fmt.Errorf("sqlite: migrate: probe sender column: %w", err)
	}
	queries := []string{`SELECT DISTINCT jid FROM jid_sessions`}
	if haveSender > 0 {
		queries = append(queries, `SELECT DISTINCT sender FROM sessions WHERE sender != ''`)
	}
	for _, q := range queries {
		rows, err := db.QueryContext(ctx, q)
		if err != nil {
			if isNoSuchTable(err) {
				continue
			}
			return nil, fmt.Errorf("sqlite: migrate: list senders: %w", err)
		}
		for rows.Next() {
			var k string
			if err := rows.Scan(&k); err != nil {
				rows.Close() //nolint:errcheck,gosec // primary error is returned
				return nil, fmt.Errorf("sqlite: migrate: scan sender: %w", err)
			}
			if !seen[k] {
				seen[k] = true
				senders = append(senders, k)
			}
		}
		rows.Close() //nolint:errcheck,gosec // iteration finished
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("sqlite: migrate: list senders: %w", err)
		}
	}
	sort.Strings(senders)
	plan, err := senderkey.PlanKeys(senders, mapping)
	if err != nil {
		return nil, fmt.Errorf("sqlite: migrate: %w", err)
	}
	rep.Keys, rep.Ambiguous = plan.Changes, plan.Ambiguous
	return plan.Keys, nil
}

func isNoSuchTable(err error) bool {
	return err != nil && bytes.Contains([]byte(err.Error()), []byte("no such table"))
}

func countPayloadMessages(ctx context.Context, db execer) (int, error) {
	n := 0
	err := eachPayload(ctx, db, func(_ string, sess *agent.Session) error {
		n += len(sess.Messages)
		return nil
	})
	return n, err
}

func eachPayload(ctx context.Context, db execer, fn func(id string, sess *agent.Session) error) error {
	rows, err := db.QueryContext(ctx, `SELECT id, payload FROM sessions ORDER BY id`)
	if err != nil {
		return fmt.Errorf("sqlite: migrate: list sessions: %w", err)
	}
	type rec struct{ id, payload string }
	var recs []rec
	for rows.Next() {
		var r rec
		if err := rows.Scan(&r.id, &r.payload); err != nil {
			rows.Close() //nolint:errcheck,gosec // primary error is returned
			return fmt.Errorf("sqlite: migrate: scan session: %w", err)
		}
		recs = append(recs, r)
	}
	rows.Close() //nolint:errcheck,gosec // iteration finished
	if err := rows.Err(); err != nil {
		return fmt.Errorf("sqlite: migrate: list sessions: %w", err)
	}
	for _, r := range recs {
		sess := &agent.Session{}
		if err := json.Unmarshal([]byte(r.payload), sess); err != nil {
			return fmt.Errorf("sqlite: migrate: session %s: unreadable payload: %w", r.id, err)
		}
		if err := fn(r.id, sess); err != nil {
			return err
		}
	}
	return nil
}

// splitPayloads writes each session's messages as rows and shrinks its
// payload to metadata. It returns each session's messages as JSON, for
// verify.
func splitPayloads(ctx context.Context, conn *sql.Conn, rep *MigrateReport) (map[string][]byte, error) {
	originals := map[string][]byte{}
	err := eachPayload(ctx, conn, func(id string, sess *agent.Session) error {
		orig, err := json.Marshal(nilIfEmpty(sess.Messages))
		if err != nil {
			return fmt.Errorf("sqlite: migrate: session %s: %w", id, err)
		}
		originals[id] = orig
		var encoded []history.Encoded
		for i, m := range sess.Messages {
			em, err := history.Encode(m)
			if err != nil {
				return fmt.Errorf("sqlite: migrate: session %s: %w", id, err)
			}
			if _, err := conn.ExecContext(ctx, `
INSERT INTO session_messages (session_id, seq, message, hash, body, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
				id, i, string(em.JSON), em.Hash, em.Body, m.CreatedAt.UTC().Format("2006-01-02T15:04:05.000Z")); err != nil {
				return fmt.Errorf("sqlite: migrate: session %s: message %d: %w", id, i, err)
			}
			encoded = append(encoded, em)
		}
		rep.Messages += len(sess.Messages)
		meta := *sess
		meta.Messages = nil
		payload, err := json.Marshal(&meta)
		if err != nil {
			return fmt.Errorf("sqlite: migrate: session %s: %w", id, err)
		}
		if _, err := conn.ExecContext(ctx, `
UPDATE sessions SET payload = ?, message_count = ?, head = '[]', base_seq = 0, next_seq = ?, view_hash = ?
WHERE id = ?`, string(payload), len(sess.Messages), len(sess.Messages), history.ViewHash(encoded, len(encoded)), id); err != nil {
			return fmt.Errorf("sqlite: migrate: session %s: %w", id, err)
		}
		return nil
	})
	return originals, err
}

func rewriteKeys(ctx context.Context, conn *sql.Conn, keys map[string]string) error {
	for from, to := range keys {
		if _, err := conn.ExecContext(ctx, `UPDATE sessions SET sender = ? WHERE sender = ?`, to, from); err != nil {
			return fmt.Errorf("sqlite: migrate: rewrite sender %s: %w", from, err)
		}
		if _, err := conn.ExecContext(ctx, `UPDATE jid_sessions SET jid = ? WHERE jid = ?`, to, from); err != nil && !isNoSuchTable(err) {
			return fmt.Errorf("sqlite: migrate: rewrite mapping %s: %w", from, err)
		}
	}
	return nil
}

// verify loads every session through the new layout and compares its
// messages with the original payload's, byte for byte, and checks no
// legacy sender key is left.
func verify(ctx context.Context, conn *sql.Conn, originals map[string][]byte) error {
	for id, want := range originals {
		sess, err := loadSession(ctx, conn, id)
		if err != nil {
			return fmt.Errorf("sqlite: migrate: verify %s: %w", id, err)
		}
		got, err := json.Marshal(nilIfEmpty(sess.Messages))
		if err != nil {
			return fmt.Errorf("sqlite: migrate: verify %s: %w", id, err)
		}
		if !bytes.Equal(got, want) {
			return fmt.Errorf("sqlite: migrate: verify %s: messages differ after migration", id)
		}
	}
	rows, err := conn.QueryContext(ctx, `SELECT DISTINCT sender FROM sessions WHERE sender != ''`)
	if err != nil {
		return fmt.Errorf("sqlite: migrate: verify senders: %w", err)
	}
	defer rows.Close() //nolint:errcheck // read-only iteration
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return fmt.Errorf("sqlite: migrate: verify senders: %w", err)
		}
		if _, _, ok := senderkey.Split(k); !ok {
			return fmt.Errorf("sqlite: migrate: verify: sender %q has no transport prefix", k)
		}
	}
	return rows.Err()
}

// backup snapshots the store next to path with VACUUM INTO and checks
// the copy's integrity. The file is private (0600).
func backup(ctx context.Context, db *sql.DB, path, label string, at time.Time) (string, error) {
	dst := fmt.Sprintf("%s.%s-%s", path, label, at.UTC().Format("20060102T150405Z"))
	if _, err := os.Stat(dst); err == nil {
		return "", fmt.Errorf("sqlite: backup: %s already exists", dst)
	}
	if _, err := db.ExecContext(ctx, `VACUUM INTO ?`, dst); err != nil {
		return "", fmt.Errorf("sqlite: backup: %w", err)
	}
	if err := os.Chmod(dst, 0o600); err != nil {
		return "", fmt.Errorf("sqlite: backup: %w", err)
	}
	bdb, err := sql.Open("sqlite", "file:"+dst+"?mode=ro")
	if err != nil {
		return "", fmt.Errorf("sqlite: backup: check: %w", err)
	}
	defer bdb.Close() //nolint:errcheck // read-only check
	var res string
	if err := bdb.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&res); err != nil {
		return "", fmt.Errorf("sqlite: backup: check: %w", err)
	}
	if res != "ok" {
		return "", fmt.Errorf("sqlite: backup: integrity_check on %s: %s", dst, res)
	}
	return dst, nil
}

// DownReport describes a MigrateDown run.
type DownReport struct {
	Sessions, Messages int
	Backup             string
}

// MigrateDown returns a version-2 store to version 1 so an older
// binary can read it: each session's view (head plus rows) is written
// back as its payload, sender keys lose their prefix, and the v2
// tables are dropped. It refuses when two transports' keys would
// collapse into one bare sender. Backed up and verified like Migrate.
func MigrateDown(ctx context.Context, path string, now func() time.Time) (DownReport, error) {
	var rep DownReport
	db, err := sql.Open("sqlite", fileDSN(path))
	if err != nil {
		return rep, fmt.Errorf("sqlite: migrate down: open: %w", err)
	}
	defer db.Close() //nolint:errcheck // nothing to report after the run
	ctx = context.WithoutCancel(ctx)
	v, err := userVersion(ctx, db)
	if err != nil {
		return rep, err
	}
	if v != schemaVersion {
		return rep, fmt.Errorf("sqlite: migrate down: store is at version %d, not %d", v, schemaVersion)
	}
	if now == nil {
		now = time.Now
	}

	bare := map[string]string{}
	rows, err := db.QueryContext(ctx, `SELECT DISTINCT sender FROM sessions WHERE sender != '' UNION SELECT DISTINCT jid FROM jid_sessions`)
	if err != nil {
		return rep, fmt.Errorf("sqlite: migrate down: list senders: %w", err)
	}
	var senders []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			rows.Close() //nolint:errcheck,gosec // primary error is returned
			return rep, fmt.Errorf("sqlite: migrate down: %w", err)
		}
		senders = append(senders, k)
	}
	rows.Close() //nolint:errcheck,gosec // iteration finished
	if err := rows.Err(); err != nil {
		return rep, fmt.Errorf("sqlite: migrate down: %w", err)
	}
	owner := map[string]string{}
	for _, k := range senders {
		b := senderkey.Bare(k)
		if prev, ok := owner[b]; ok && prev != k {
			return rep, fmt.Errorf("sqlite: migrate down: %q and %q would both become %q", prev, k, b)
		}
		owner[b] = k
		if b != k {
			bare[k] = b
		}
	}

	if rep.Backup, err = backup(ctx, db, path, "pre-down", now()); err != nil {
		return rep, err
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return rep, fmt.Errorf("sqlite: migrate down: conn: %w", err)
	}
	defer conn.Close() //nolint:errcheck // returns the connection to the pool
	if _, err := conn.ExecContext(ctx, `BEGIN EXCLUSIVE`); err != nil {
		return rep, fmt.Errorf("sqlite: migrate down: lock (is a daemon still running?): %w", err)
	}
	done := false
	defer func() {
		if !done {
			_, _ = conn.ExecContext(context.Background(), `ROLLBACK`) //nolint:errcheck // the primary error is returned
		}
	}()

	ids, err := queryStrings(ctx, conn, `SELECT id FROM sessions ORDER BY id`)
	if err != nil {
		return rep, fmt.Errorf("sqlite: migrate down: %w", err)
	}
	for _, id := range ids {
		sess, err := loadSession(ctx, conn, id)
		if err != nil {
			return rep, fmt.Errorf("sqlite: migrate down: %s: %w", id, err)
		}
		sess.Sender = senderkey.Bare(sess.Sender)
		payload, err := json.Marshal(sess)
		if err != nil {
			return rep, fmt.Errorf("sqlite: migrate down: %s: %w", id, err)
		}
		if _, err := conn.ExecContext(ctx,
			`UPDATE sessions SET payload = ?, message_count = ?, search_text = ?, sender = ? WHERE id = ?`,
			string(payload), len(sess.Messages), searchText(sess), sess.Sender, id); err != nil {
			return rep, fmt.Errorf("sqlite: migrate down: %s: %w", id, err)
		}
		rep.Sessions++
		rep.Messages += len(sess.Messages)
	}
	for k, b := range bare {
		if _, err := conn.ExecContext(ctx, `UPDATE jid_sessions SET jid = ? WHERE jid = ?`, b, k); err != nil {
			return rep, fmt.Errorf("sqlite: migrate down: mapping %s: %w", k, err)
		}
	}
	for _, q := range []string{
		`DROP TRIGGER IF EXISTS session_messages_fts_ai`,
		`DROP TRIGGER IF EXISTS session_messages_fts_ad`,
		`DROP TRIGGER IF EXISTS sessions_titles_ai`,
		`DROP TRIGGER IF EXISTS sessions_titles_au`,
		`DROP TRIGGER IF EXISTS sessions_titles_ad`,
		`DROP TABLE IF EXISTS messages_fts`,
		`DROP TABLE IF EXISTS titles_fts`,
		`DROP TABLE IF EXISTS session_messages`,
		`ALTER TABLE sessions DROP COLUMN head`,
		`ALTER TABLE sessions DROP COLUMN base_seq`,
		`ALTER TABLE sessions DROP COLUMN next_seq`,
		`ALTER TABLE sessions DROP COLUMN view_hash`,
		`PRAGMA user_version = 1`,
	} {
		if _, err := conn.ExecContext(ctx, q); err != nil {
			return rep, fmt.Errorf("sqlite: migrate down: %s: %w", q, err)
		}
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return rep, fmt.Errorf("sqlite: migrate down: commit: %w", err)
	}
	done = true
	return rep, nil
}

// nilIfEmpty makes an empty message list compare equal to a nil one:
// v1 payloads hold either "messages":null or "messages":[].
func nilIfEmpty(m []agent.Message) []agent.Message {
	if len(m) == 0 {
		return nil
	}
	return m
}
