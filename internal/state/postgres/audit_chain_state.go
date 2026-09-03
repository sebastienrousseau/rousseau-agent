package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/sebastienrousseau/rousseau-agent/internal/observability/audit_egress"
)

// auditChainSchema is the Postgres-flavoured mirror of the SQLite
// schema. Differences worth calling out:
//
//   - BIGINT for last_sequence — audit chains can outlive an
//     INTEGER (~2^31) at high volumes. SQLite gets away with
//     `INTEGER` because integer affinity accepts 64-bit values;
//     Postgres INTEGER hard-caps at 2^31. BIGINT matches the
//     uint64 the [audit_egress.ChainStore] interface returns.
//   - TIMESTAMPTZ + DEFAULT NOW() for updated_at — same pattern
//     as the rest of the §2.4a ports.
//   - CHECK (id = 1) preserved — single-row-only enforcement so
//     a bug that tries to write a second row surfaces at INSERT
//     time rather than corrupting the chain.
//
// Multi-replica note: this table has to be atomic across writers
// on the same DB. The INSERT ... ON CONFLICT DO UPDATE is atomic
// under REPEATABLE READ; concurrent writers race on the id=1 row
// and the last-committer's (sequence, hash) wins. If both writers
// have valid follow-on hashes, the SIEM sees a chain break AT
// the losing writer — visible, not silent. That is the same
// contract the SQLite driver ships (single-writer today; the
// chain-break signal is the safety net if that assumption ever
// breaks).
const auditChainSchema = `
CREATE TABLE IF NOT EXISTS audit_chain_state (
    id            INTEGER PRIMARY KEY CHECK (id = 1),
    last_sequence BIGINT NOT NULL DEFAULT 0,
    last_hash     TEXT NOT NULL DEFAULT '',
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
`

// AuditChainState is a Postgres-backed [audit_egress.ChainStore].
// Interface-compatible with [sqlite.AuditChainState] via the
// shared audit_egress.ChainStore interface.
type AuditChainState struct {
	db *sql.DB
}

// NewAuditChainState attaches to an existing Postgres [Store] and
// applies the schema. Idempotent — safe to call on every daemon
// boot.
func NewAuditChainState(ctx context.Context, s *Store) (*AuditChainState, error) {
	if _, err := s.db.ExecContext(ctx, auditChainSchema); err != nil {
		return nil, fmt.Errorf("postgres: apply audit_chain_state schema: %w", err)
	}
	return &AuditChainState{db: s.db}, nil
}

// Load satisfies [audit_egress.ChainStore]. Returns (0, "", nil)
// when the table is empty (fresh install / never emitted) —
// callers treat that as "start a fresh chain". Matches the
// SQLite driver's semantics exactly.
func (a *AuditChainState) Load(ctx context.Context) (uint64, string, error) {
	const q = `SELECT last_sequence, last_hash FROM audit_chain_state WHERE id = 1`
	var (
		seq  int64 // Postgres BIGINT is signed; convert to uint64 below
		hash string
	)
	err := a.db.QueryRowContext(ctx, q).Scan(&seq, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, "", nil
	}
	if err != nil {
		return 0, "", fmt.Errorf("postgres: load audit chain state: %w", err)
	}
	if seq < 0 {
		// Defensive: a corrupted row shouldn't wedge the daemon.
		// Treat as "start fresh" — the SIEM sees a chain break,
		// which is more visible than silently proceeding with
		// bogus state. Same behaviour as the SQLite driver.
		return 0, "", nil
	}
	return uint64(seq), hash, nil
}

// Save satisfies [audit_egress.ChainStore]. Atomic upsert on the
// fixed id=1 row — a concurrent writer's win is a normal race
// outcome, not an error condition, and the SIEM would flag the
// resulting chain break at the losing writer's next record.
func (a *AuditChainState) Save(ctx context.Context, sequence uint64, hash string) error {
	const q = `
INSERT INTO audit_chain_state (id, last_sequence, last_hash, updated_at)
VALUES (1, $1, $2, NOW())
ON CONFLICT (id) DO UPDATE SET
    last_sequence = EXCLUDED.last_sequence,
    last_hash     = EXCLUDED.last_hash,
    updated_at    = EXCLUDED.updated_at
`
	if _, err := a.db.ExecContext(ctx, q, int64(sequence), hash); err != nil { //nolint:gosec // seq fits comfortably in int64 for the daemon's lifetime
		return fmt.Errorf("postgres: save audit chain state: %w", err)
	}
	return nil
}

// Compile-time interface satisfaction — matches the SQLite
// driver's assertion so both concrete types can be handed to
// callers expecting [audit_egress.ChainStore].
var _ audit_egress.ChainStore = (*AuditChainState)(nil)
