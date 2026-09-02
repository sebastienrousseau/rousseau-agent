package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/sebastienrousseau/rousseau-agent/internal/observability/audit_egress"
)

// auditChainSchema stores the tail of the hash-chained audit log
// so a [audit_egress.ChainedSink] can resume across daemon
// restarts. Single-row-only (enforced by the CHECK constraint)
// — there's exactly one active chain per daemon.
//
// The `updated_at` column exists for operator debugging ("when
// did the daemon last write an audit record?"), not for the
// verifier — the SIEM has its own arrival timestamps.
const auditChainSchema = `
CREATE TABLE IF NOT EXISTS audit_chain_state (
    id             INTEGER PRIMARY KEY CHECK (id = 1),
    last_sequence  INTEGER NOT NULL DEFAULT 0,
    last_hash      TEXT NOT NULL DEFAULT '',
    updated_at     TEXT NOT NULL
);
`

// AuditChainState is a SQLite-backed [audit_egress.ChainStore].
type AuditChainState struct {
	db *sql.DB
}

// NewAuditChainState returns a store that shares s's SQLite
// database.
func NewAuditChainState(ctx context.Context, s *Store) (*AuditChainState, error) {
	if _, err := s.db.ExecContext(ctx, auditChainSchema); err != nil {
		return nil, fmt.Errorf("sqlite: apply audit_chain_state schema: %w", err)
	}
	return &AuditChainState{db: s.db}, nil
}

// Load satisfies [audit_egress.ChainStore]. Returns (0, "", nil)
// when the table is empty (fresh install / never emitted) —
// callers treat that as "start a fresh chain".
func (a *AuditChainState) Load(ctx context.Context) (uint64, string, error) {
	const q = `SELECT last_sequence, last_hash FROM audit_chain_state WHERE id = 1`
	var seq int64 // sqlite stores integers signed; convert to uint64 below
	var hash string
	err := a.db.QueryRowContext(ctx, q).Scan(&seq, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, "", nil
	}
	if err != nil {
		return 0, "", fmt.Errorf("sqlite: load audit chain state: %w", err)
	}
	if seq < 0 {
		// Defensive: a corrupted row shouldn't wedge the daemon.
		// Treat as "start fresh" — the SIEM sees a chain break,
		// which is more visible than silently proceeding with
		// bogus state.
		return 0, "", nil
	}
	return uint64(seq), hash, nil
}

// Save satisfies [audit_egress.ChainStore]. Uses INSERT OR
// REPLACE on the fixed id=1 row so the write is atomic against
// concurrent readers of the same row.
func (a *AuditChainState) Save(ctx context.Context, sequence uint64, hash string) error {
	const q = `
INSERT INTO audit_chain_state (id, last_sequence, last_hash, updated_at)
VALUES (1, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
    last_sequence = excluded.last_sequence,
    last_hash     = excluded.last_hash,
    updated_at    = excluded.updated_at
`
	now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	if _, err := a.db.ExecContext(ctx, q, int64(sequence), hash, now); err != nil { //nolint:gosec // seq fits comfortably in int64 for the daemon's lifetime
		return fmt.Errorf("sqlite: save audit chain state: %w", err)
	}
	return nil
}

// Compile-time interface satisfaction.
var _ audit_egress.ChainStore = (*AuditChainState)(nil)
