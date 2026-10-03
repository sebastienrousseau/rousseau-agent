package postgres

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
	"github.com/sebastienrousseau/rousseau-agent/internal/senderkey"
	"github.com/sebastienrousseau/rousseau-agent/internal/state/history"
	sqlitestore "github.com/sebastienrousseau/rousseau-agent/internal/state/sqlite"
)

// MigrateReport is shared with the SQLite driver.
type MigrateReport = sqlitestore.MigrateReport

// DownReport is shared with the SQLite driver.
type DownReport = sqlitestore.DownReport

// ErrAmbiguousSenders is returned when a legacy key's transport cannot
// be inferred and no mapping was given.
var ErrAmbiguousSenders = sqlitestore.ErrAmbiguousSenders

// ErrBackupRequired is returned by a real (non-dry) run until the
// operator confirms a backup: rousseau cannot snapshot a Postgres
// database itself the way it does a SQLite file.
var ErrBackupRequired = errors.New("postgres: migrate: take a backup first (pg_dump --format=custom --file=rousseau-pre-v2.dump <database>), then rerun with --backup-taken")

// MigrateOptions controls Migrate.
type MigrateOptions struct {
	DryRun bool
	Map    map[string]string
	// BackupTaken confirms the operator has a pg_dump of the database.
	BackupTaken bool
}

// Migrate upgrades the store at dsn to schema version 2, like the
// SQLite driver's Migrate: one transaction (Postgres DDL is
// transactional), every session verified before commit, and nothing
// changed on any failure.
func Migrate(ctx context.Context, dsn string, opts MigrateOptions) (MigrateReport, error) {
	rep := MigrateReport{ToVersion: schemaVersion}
	db, err := openRaw(ctx, dsn)
	if err != nil {
		return rep, err
	}
	defer db.Close() //nolint:errcheck // nothing to report after the run
	ctx = context.WithoutCancel(ctx)

	if rep.FromVersion, err = peekVersion(ctx, db); err != nil {
		return rep, err
	}
	if rep.FromVersion >= schemaVersion {
		rep.ToVersion = rep.FromVersion
		return rep, nil
	}
	senders, err := legacySenders(ctx, db)
	if err != nil {
		return rep, err
	}
	plan, err := senderkey.PlanKeys(senders, opts.Map)
	if err != nil {
		return rep, fmt.Errorf("postgres: migrate: %w", err)
	}
	rep.Keys, rep.Ambiguous = plan.Changes, plan.Ambiguous
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions`).Scan(&rep.Sessions); err != nil {
		return rep, fmt.Errorf("postgres: migrate: %w", err)
	}
	if opts.DryRun {
		err := eachPayload(ctx, db, func(_ string, sess *agent.Session) error {
			rep.Messages += len(sess.Messages)
			return nil
		})
		return rep, err
	}
	if len(rep.Ambiguous) > 0 {
		return rep, ErrAmbiguousSenders
	}
	if !opts.BackupTaken {
		return rep, ErrBackupRequired
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return rep, fmt.Errorf("postgres: migrate: begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	if _, err := tx.ExecContext(ctx, `LOCK TABLE sessions IN ACCESS EXCLUSIVE MODE`); err != nil {
		return rep, fmt.Errorf("postgres: migrate: lock: %w", err)
	}
	if _, err := tx.ExecContext(ctx, v2Schema); err != nil {
		return rep, fmt.Errorf("postgres: migrate: schema: %w", err)
	}
	originals := map[string][]byte{}
	err = eachPayload(ctx, tx, func(id string, sess *agent.Session) error {
		orig, err := json.Marshal(nilIfEmpty(sess.Messages))
		if err != nil {
			return fmt.Errorf("postgres: migrate: session %s: %w", id, err)
		}
		originals[id] = orig
		var encoded []history.Encoded
		for i, m := range sess.Messages {
			em, err := history.Encode(m)
			if err != nil {
				return fmt.Errorf("postgres: migrate: session %s: %w", id, err)
			}
			if _, err := tx.ExecContext(ctx, `
INSERT INTO session_messages (session_id, seq, message, hash, body, created_at) VALUES ($1, $2, $3, $4, $5, $6)`,
				id, i, string(em.JSON), em.Hash, em.Body, m.CreatedAt.UTC()); err != nil {
				return fmt.Errorf("postgres: migrate: session %s: message %d: %w", id, i, err)
			}
			encoded = append(encoded, em)
		}
		rep.Messages += len(sess.Messages)
		meta := *sess
		meta.Messages = nil
		payload, err := json.Marshal(&meta)
		if err != nil {
			return fmt.Errorf("postgres: migrate: session %s: %w", id, err)
		}
		if _, err := tx.ExecContext(ctx, `
UPDATE sessions SET payload = $1, message_count = $2, head = '[]', base_seq = 0, next_seq = $3, view_hash = $4
WHERE id = $5`, string(payload), len(sess.Messages), int64(len(sess.Messages)), history.ViewHash(encoded, len(encoded)), id); err != nil {
			return fmt.Errorf("postgres: migrate: session %s: %w", id, err)
		}
		return nil
	})
	if err != nil {
		return rep, err
	}
	haveJID, err := tableExists(ctx, tx, "jid_sessions")
	if err != nil {
		return rep, err
	}
	for from, to := range plan.Keys {
		if _, err := tx.ExecContext(ctx, `UPDATE sessions SET sender = $1 WHERE sender = $2`, to, from); err != nil {
			return rep, fmt.Errorf("postgres: migrate: rewrite sender %s: %w", from, err)
		}
		if haveJID {
			if _, err := tx.ExecContext(ctx, `UPDATE jid_sessions SET jid = $1 WHERE jid = $2`, to, from); err != nil {
				return rep, fmt.Errorf("postgres: migrate: rewrite mapping %s: %w", from, err)
			}
		}
	}
	for id, want := range originals {
		sess, err := loadSession(ctx, tx, id)
		if err != nil {
			return rep, fmt.Errorf("postgres: migrate: verify %s: %w", id, err)
		}
		got, err := json.Marshal(nilIfEmpty(sess.Messages))
		if err != nil {
			return rep, fmt.Errorf("postgres: migrate: verify %s: %w", id, err)
		}
		if !bytes.Equal(got, want) {
			return rep, fmt.Errorf("postgres: migrate: verify %s: messages differ after migration", id)
		}
	}
	if _, err := tx.ExecContext(ctx, versionTableDDL); err != nil {
		return rep, fmt.Errorf("postgres: migrate: version table: %w", err)
	}
	if err := setVersion(ctx, tx, schemaVersion); err != nil {
		return rep, err
	}
	if err := tx.Commit(); err != nil {
		return rep, fmt.Errorf("postgres: migrate: commit: %w", err)
	}
	return rep, nil
}

// MigrateDown returns a version-2 store to version 1, like the SQLite
// driver's MigrateDown. backupTaken confirms a pg_dump exists.
func MigrateDown(ctx context.Context, dsn string, backupTaken bool) (DownReport, error) {
	var rep DownReport
	db, err := openRaw(ctx, dsn)
	if err != nil {
		return rep, err
	}
	defer db.Close() //nolint:errcheck // nothing to report after the run
	ctx = context.WithoutCancel(ctx)
	v, err := peekVersion(ctx, db)
	if err != nil {
		return rep, err
	}
	if v != schemaVersion {
		return rep, fmt.Errorf("postgres: migrate down: store is at version %d, not %d", v, schemaVersion)
	}
	if !backupTaken {
		return rep, ErrBackupRequired
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return rep, fmt.Errorf("postgres: migrate down: begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	if _, err := tx.ExecContext(ctx, `LOCK TABLE sessions IN ACCESS EXCLUSIVE MODE`); err != nil {
		return rep, fmt.Errorf("postgres: migrate down: lock: %w", err)
	}
	keys, err := legacySenders(ctx, tx)
	if err != nil {
		return rep, err
	}
	owner := map[string]string{}
	for _, k := range keys {
		b := senderkey.Bare(k)
		if prev, ok := owner[b]; ok && prev != k {
			return rep, fmt.Errorf("postgres: migrate down: %q and %q would both become %q", prev, k, b)
		}
		owner[b] = k
	}
	ids, err := queryStrings(ctx, tx, `SELECT id FROM sessions ORDER BY id`)
	if err != nil {
		return rep, fmt.Errorf("postgres: migrate down: %w", err)
	}
	for _, id := range ids {
		sess, err := loadSession(ctx, tx, id)
		if err != nil {
			return rep, fmt.Errorf("postgres: migrate down: %s: %w", id, err)
		}
		sess.Sender = senderkey.Bare(sess.Sender)
		payload, err := json.Marshal(sess)
		if err != nil {
			return rep, fmt.Errorf("postgres: migrate down: %s: %w", id, err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE sessions SET payload = $1, message_count = $2, sender = $3 WHERE id = $4`,
			string(payload), len(sess.Messages), sess.Sender, id); err != nil {
			return rep, fmt.Errorf("postgres: migrate down: %s: %w", id, err)
		}
		rep.Sessions++
		rep.Messages += len(sess.Messages)
	}
	for _, k := range keys {
		if b := senderkey.Bare(k); b != k {
			if _, err := tx.ExecContext(ctx, `UPDATE jid_sessions SET jid = $1 WHERE jid = $2`, b, k); err != nil {
				return rep, fmt.Errorf("postgres: migrate down: mapping %s: %w", k, err)
			}
		}
	}
	if _, err := tx.ExecContext(ctx, `
DROP TABLE IF EXISTS session_messages;
DROP INDEX IF EXISTS idx_sessions_title_vector;
ALTER TABLE sessions DROP COLUMN IF EXISTS title_vector;
ALTER TABLE sessions DROP COLUMN IF EXISTS head;
ALTER TABLE sessions DROP COLUMN IF EXISTS base_seq;
ALTER TABLE sessions DROP COLUMN IF EXISTS next_seq;
ALTER TABLE sessions DROP COLUMN IF EXISTS view_hash;
DELETE FROM rousseau_schema;`); err != nil {
		return rep, fmt.Errorf("postgres: migrate down: drop v2: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return rep, fmt.Errorf("postgres: migrate down: commit: %w", err)
	}
	return rep, nil
}

func openRaw(ctx context.Context, dsn string) (*sql.DB, error) {
	if dsn == "" {
		return nil, errors.New("postgres: empty DSN")
	}
	db, err := openDB("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("postgres: open: %w", err)
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close() //nolint:errcheck // primary error already returned
		return nil, fmt.Errorf("postgres: ping: %w", err)
	}
	return db, nil
}

func tableExists(ctx context.Context, db querier, name string) (bool, error) {
	var ok bool
	if err := db.QueryRowContext(ctx, `SELECT to_regclass($1) IS NOT NULL`, name).Scan(&ok); err != nil {
		return false, fmt.Errorf("postgres: probe %s: %w", name, err)
	}
	return ok, nil
}

// peekVersion reads the schema version without creating anything, so
// a dry run writes nothing.
func peekVersion(ctx context.Context, db querier) (int, error) {
	ok, err := tableExists(ctx, db, "rousseau_schema")
	if err != nil || !ok {
		return 1, err
	}
	var v int
	err = db.QueryRowContext(ctx, `SELECT version FROM rousseau_schema WHERE id = 1`).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return 1, nil
	}
	if err != nil {
		return 0, fmt.Errorf("postgres: read version: %w", err)
	}
	return v, nil
}

func legacySenders(ctx context.Context, db querier) ([]string, error) {
	q := `SELECT DISTINCT sender FROM sessions WHERE sender <> ''`
	haveJID, err := tableExists(ctx, db, "jid_sessions")
	if err != nil {
		return nil, err
	}
	if haveJID {
		q += ` UNION SELECT DISTINCT jid FROM jid_sessions`
	}
	out, err := queryStrings(ctx, db, q)
	if err != nil {
		return nil, fmt.Errorf("postgres: migrate: list senders: %w", err)
	}
	sort.Strings(out)
	return out, nil
}

func queryStrings(ctx context.Context, db querier, q string, args ...any) ([]string, error) {
	rows, err := db.QueryContext(ctx, q, args...)
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

func eachPayload(ctx context.Context, db querier, fn func(id string, sess *agent.Session) error) error {
	type rec struct{ id, payload string }
	rows, err := db.QueryContext(ctx, `SELECT id, payload FROM sessions ORDER BY id`)
	if err != nil {
		return fmt.Errorf("postgres: migrate: list sessions: %w", err)
	}
	var recs []rec
	for rows.Next() {
		var r rec
		if err := rows.Scan(&r.id, &r.payload); err != nil {
			rows.Close() //nolint:errcheck,gosec // primary error is returned
			return fmt.Errorf("postgres: migrate: scan session: %w", err)
		}
		recs = append(recs, r)
	}
	rows.Close() //nolint:errcheck,gosec // iteration finished
	if err := rows.Err(); err != nil {
		return fmt.Errorf("postgres: migrate: list sessions: %w", err)
	}
	for _, r := range recs {
		sess := &agent.Session{}
		if err := json.Unmarshal([]byte(r.payload), sess); err != nil {
			return fmt.Errorf("postgres: migrate: session %s: unreadable payload: %w", r.id, err)
		}
		if err := fn(r.id, sess); err != nil {
			return err
		}
	}
	return nil
}

// nilIfEmpty makes an empty message list compare equal to a nil one:
// v1 payloads hold either "messages":null or "messages":[].
func nilIfEmpty(m []agent.Message) []agent.Message {
	if len(m) == 0 {
		return nil
	}
	return m
}
