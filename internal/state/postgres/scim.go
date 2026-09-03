package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/sebastienrousseau/rousseau-agent/internal/auth/scim"
)

// scimSchema is the Postgres-flavoured mirror of the SQLite
// schema. Differences worth calling out:
//
//   - JSONB for user + group bodies (SQLite uses TEXT). JSONB
//     validates + normalises at insert time, catching a malformed
//     payload immediately rather than at the next unmarshal.
//   - TIMESTAMPTZ + DEFAULT NOW() for created_at / updated_at —
//     matches the pattern established by the other §2.4a ports.
//   - external_id lifted to NULL (rather than the SQLite driver's
//     nil-any dance in application code) — Postgres UNIQUE
//     indexes treat multiple NULLs as distinct, so multiple users
//     may have no externalId without colliding. Same behaviour
//     as SQLite; explicit here.
//   - ON DELETE CASCADE preserved on the memberships FK — Postgres
//     enforces FK unconditionally; SQLite needs `foreign_keys=ON`.
const scimSchema = `
CREATE TABLE IF NOT EXISTS scim_users (
    id          TEXT PRIMARY KEY,
    user_name   TEXT NOT NULL UNIQUE,
    external_id TEXT UNIQUE,
    body        JSONB NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS scim_groups (
    id           TEXT PRIMARY KEY,
    display_name TEXT NOT NULL UNIQUE,
    external_id  TEXT UNIQUE,
    body         JSONB NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS scim_group_members (
    group_id TEXT NOT NULL REFERENCES scim_groups(id) ON DELETE CASCADE,
    user_id  TEXT NOT NULL REFERENCES scim_users(id)  ON DELETE CASCADE,
    PRIMARY KEY (group_id, user_id)
);

CREATE INDEX IF NOT EXISTS idx_scim_group_members_user
    ON scim_group_members(user_id);
`

// SCIMStore is a Postgres-backed [scim.Store]. Interface-
// compatible with [sqlite.SCIMStore] via the shared scim.Store
// interface — daemon can hold either concrete type.
type SCIMStore struct {
	db *sql.DB
}

// NewSCIMStore attaches to an existing Postgres [Store] and
// applies the schema. Idempotent — safe to call on every daemon
// boot.
func NewSCIMStore(ctx context.Context, s *Store) (*SCIMStore, error) {
	if _, err := s.db.ExecContext(ctx, scimSchema); err != nil {
		return nil, fmt.Errorf("postgres: apply scim schema: %w", err)
	}
	return &SCIMStore{db: s.db}, nil
}

// -- User operations --

// CreateUser satisfies [scim.Store]. Populates the SCIM ID
// (UUID v4) if the caller didn't supply one. Active defaults to
// true per SCIM 2.0 §3.
func (s *SCIMStore) CreateUser(ctx context.Context, u scim.User) (scim.User, error) {
	if u.ID == "" {
		u.ID = uuid.NewString()
	}
	u.Active = true
	body, err := json.Marshal(u)
	if err != nil {
		return scim.User{}, fmt.Errorf("postgres: marshal scim user: %w", err)
	}
	const q = `
INSERT INTO scim_users (id, user_name, external_id, body)
VALUES ($1, $2, $3, $4::jsonb)
`
	if _, err := s.db.ExecContext(ctx, q, u.ID, u.UserName, nullIfEmpty(u.ExternalID), string(body)); err != nil {
		if isUniqueViolation(err) {
			return scim.User{}, fmt.Errorf("%w: userName or externalId already exists", scim.ErrConflict)
		}
		return scim.User{}, fmt.Errorf("postgres: create scim user: %w", err)
	}
	return u, nil
}

// GetUser satisfies [scim.Store].
func (s *SCIMStore) GetUser(ctx context.Context, id string) (scim.User, error) {
	const q = `SELECT body FROM scim_users WHERE id = $1`
	var body []byte
	err := s.db.QueryRowContext(ctx, q, id).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return scim.User{}, scim.ErrNotFound
	}
	if err != nil {
		return scim.User{}, fmt.Errorf("postgres: get scim user: %w", err)
	}
	return unmarshalUser(body)
}

// ListUsers satisfies [scim.Store]. Pagination is SCIM's 1-based
// startIndex; count=0 disables the cap.
func (s *SCIMStore) ListUsers(ctx context.Context, filterUserName string, startIndex, count int) ([]scim.User, int, error) {
	countQuery := `SELECT COUNT(*) FROM scim_users`
	listQuery := `SELECT body FROM scim_users`
	var (
		args      []any
		countArgs []any
		next      = 1
	)
	if filterUserName != "" {
		countQuery += ` WHERE user_name = $1`
		listQuery += ` WHERE user_name = $1`
		args = append(args, filterUserName)
		countArgs = append(countArgs, filterUserName)
		next++
	}
	listQuery += ` ORDER BY user_name`
	if count > 0 {
		listQuery += fmt.Sprintf(` LIMIT $%d OFFSET $%d`, next, next+1)
		offset := startIndex - 1
		if offset < 0 {
			offset = 0
		}
		args = append(args, count, offset)
	}

	var total int
	if err := s.db.QueryRowContext(ctx, countQuery, countArgs...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("postgres: count scim users: %w", err)
	}

	rows, err := s.db.QueryContext(ctx, listQuery, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("postgres: list scim users: %w", err)
	}
	defer func() { _ = rows.Close() }() //nolint:errcheck // best-effort cleanup

	var out []scim.User
	for rows.Next() {
		var body []byte
		if err := rows.Scan(&body); err != nil {
			return nil, 0, fmt.Errorf("postgres: scan scim user: %w", err)
		}
		u, err := unmarshalUser(body)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, u)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("postgres: iterate scim users: %w", err)
	}
	return out, total, nil
}

// ReplaceUser satisfies [scim.Store]. Existence-then-update so
// missing IDs surface as ErrNotFound rather than a silent no-op.
func (s *SCIMStore) ReplaceUser(ctx context.Context, id string, u scim.User) (scim.User, error) {
	if _, err := s.GetUser(ctx, id); err != nil {
		return scim.User{}, err
	}
	u.ID = id
	body, err := json.Marshal(u)
	if err != nil {
		return scim.User{}, fmt.Errorf("postgres: marshal scim user: %w", err)
	}
	const q = `
UPDATE scim_users
SET user_name = $1, external_id = $2, body = $3::jsonb, updated_at = NOW()
WHERE id = $4
`
	if _, err := s.db.ExecContext(ctx, q, u.UserName, nullIfEmpty(u.ExternalID), string(body), id); err != nil {
		if isUniqueViolation(err) {
			return scim.User{}, fmt.Errorf("%w: userName or externalId collision", scim.ErrConflict)
		}
		return scim.User{}, fmt.Errorf("postgres: replace scim user: %w", err)
	}
	return u, nil
}

// DeleteUser satisfies [scim.Store]. Idempotent per SCIM 2.0
// §3.6 — missing ID returns nil.
func (s *SCIMStore) DeleteUser(ctx context.Context, id string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM scim_users WHERE id = $1`, id); err != nil {
		return fmt.Errorf("postgres: delete scim user: %w", err)
	}
	return nil
}

// -- Group operations --

// CreateGroup satisfies [scim.Store]. Group row + memberships
// go in a single transaction so a partial failure never leaves
// a group without its membership rows.
func (s *SCIMStore) CreateGroup(ctx context.Context, g scim.Group) (scim.Group, error) {
	if g.ID == "" {
		g.ID = uuid.NewString()
	}
	body, err := json.Marshal(g)
	if err != nil {
		return scim.Group{}, fmt.Errorf("postgres: marshal scim group: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return scim.Group{}, fmt.Errorf("postgres: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }() //nolint:errcheck // rollback is idempotent

	const insertQ = `
INSERT INTO scim_groups (id, display_name, external_id, body)
VALUES ($1, $2, $3, $4::jsonb)
`
	if _, err := tx.ExecContext(ctx, insertQ, g.ID, g.DisplayName, nullIfEmpty(g.ExternalID), string(body)); err != nil {
		if isUniqueViolation(err) {
			return scim.Group{}, fmt.Errorf("%w: displayName or externalId already exists", scim.ErrConflict)
		}
		return scim.Group{}, fmt.Errorf("postgres: create scim group: %w", err)
	}
	if err := replaceMemberships(ctx, tx, g.ID, g.Members); err != nil {
		return scim.Group{}, err
	}
	if err := tx.Commit(); err != nil {
		return scim.Group{}, fmt.Errorf("postgres: commit create scim group: %w", err)
	}
	return g, nil
}

// GetGroup satisfies [scim.Store].
func (s *SCIMStore) GetGroup(ctx context.Context, id string) (scim.Group, error) {
	const q = `SELECT body FROM scim_groups WHERE id = $1`
	var body []byte
	err := s.db.QueryRowContext(ctx, q, id).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return scim.Group{}, scim.ErrNotFound
	}
	if err != nil {
		return scim.Group{}, fmt.Errorf("postgres: get scim group: %w", err)
	}
	return unmarshalGroup(body)
}

// ListGroups satisfies [scim.Store].
func (s *SCIMStore) ListGroups(ctx context.Context, filterDisplayName string, startIndex, count int) ([]scim.Group, int, error) {
	countQuery := `SELECT COUNT(*) FROM scim_groups`
	listQuery := `SELECT body FROM scim_groups`
	var (
		args      []any
		countArgs []any
		next      = 1
	)
	if filterDisplayName != "" {
		countQuery += ` WHERE display_name = $1`
		listQuery += ` WHERE display_name = $1`
		args = append(args, filterDisplayName)
		countArgs = append(countArgs, filterDisplayName)
		next++
	}
	listQuery += ` ORDER BY display_name`
	if count > 0 {
		listQuery += fmt.Sprintf(` LIMIT $%d OFFSET $%d`, next, next+1)
		offset := startIndex - 1
		if offset < 0 {
			offset = 0
		}
		args = append(args, count, offset)
	}

	var total int
	if err := s.db.QueryRowContext(ctx, countQuery, countArgs...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("postgres: count scim groups: %w", err)
	}

	rows, err := s.db.QueryContext(ctx, listQuery, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("postgres: list scim groups: %w", err)
	}
	defer func() { _ = rows.Close() }() //nolint:errcheck // best-effort cleanup

	var out []scim.Group
	for rows.Next() {
		var body []byte
		if err := rows.Scan(&body); err != nil {
			return nil, 0, fmt.Errorf("postgres: scan scim group: %w", err)
		}
		g, err := unmarshalGroup(body)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, g)
	}
	return out, total, nil
}

// ReplaceGroup satisfies [scim.Store].
func (s *SCIMStore) ReplaceGroup(ctx context.Context, id string, g scim.Group) (scim.Group, error) {
	if _, err := s.GetGroup(ctx, id); err != nil {
		return scim.Group{}, err
	}
	g.ID = id
	body, err := json.Marshal(g)
	if err != nil {
		return scim.Group{}, fmt.Errorf("postgres: marshal scim group: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return scim.Group{}, fmt.Errorf("postgres: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }() //nolint:errcheck // idempotent
	const q = `
UPDATE scim_groups
SET display_name = $1, external_id = $2, body = $3::jsonb, updated_at = NOW()
WHERE id = $4
`
	if _, err := tx.ExecContext(ctx, q, g.DisplayName, nullIfEmpty(g.ExternalID), string(body), id); err != nil {
		if isUniqueViolation(err) {
			return scim.Group{}, fmt.Errorf("%w: displayName or externalId collision", scim.ErrConflict)
		}
		return scim.Group{}, fmt.Errorf("postgres: replace scim group: %w", err)
	}
	if err := replaceMemberships(ctx, tx, id, g.Members); err != nil {
		return scim.Group{}, err
	}
	if err := tx.Commit(); err != nil {
		return scim.Group{}, fmt.Errorf("postgres: commit replace scim group: %w", err)
	}
	return g, nil
}

// DeleteGroup satisfies [scim.Store]. Idempotent — memberships
// are cascaded automatically via the FK.
func (s *SCIMStore) DeleteGroup(ctx context.Context, id string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM scim_groups WHERE id = $1`, id); err != nil {
		return fmt.Errorf("postgres: delete scim group: %w", err)
	}
	return nil
}

// -- Cross-cutting --

// LookupUserByExternalID satisfies [scim.Store].
func (s *SCIMStore) LookupUserByExternalID(ctx context.Context, externalID string) (scim.User, error) {
	if externalID == "" {
		return scim.User{}, scim.ErrNotFound
	}
	const q = `SELECT body FROM scim_users WHERE external_id = $1`
	var body []byte
	err := s.db.QueryRowContext(ctx, q, externalID).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return scim.User{}, scim.ErrNotFound
	}
	if err != nil {
		return scim.User{}, fmt.Errorf("postgres: lookup scim user by externalId: %w", err)
	}
	return unmarshalUser(body)
}

// UserGroupNames returns the displayNames of every group the
// user belongs to, sorted alphabetically. Returns nil when the
// user has no memberships or doesn't exist — same shape either
// way, matches what downstream RBAC / OPA policies expect.
//
// Not part of the [scim.Store] interface — exposed for the SSO
// ResolveTransportID adapter (cli/sso_scim_adapter.go).
func (s *SCIMStore) UserGroupNames(ctx context.Context, userID string) ([]string, error) {
	if userID == "" {
		return nil, nil
	}
	const q = `
SELECT g.display_name
FROM scim_groups g
JOIN scim_group_members m ON m.group_id = g.id
WHERE m.user_id = $1
ORDER BY g.display_name
`
	rows, err := s.db.QueryContext(ctx, q, userID)
	if err != nil {
		return nil, fmt.Errorf("postgres: user group names: %w", err)
	}
	defer func() { _ = rows.Close() }() //nolint:errcheck // best-effort cleanup

	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("postgres: scan group name: %w", err)
		}
		out = append(out, name)
	}
	return out, nil
}

// Count satisfies [scim.Store]. Used by doctor.
func (s *SCIMStore) Count(ctx context.Context) (int, int, error) {
	var users, groups int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM scim_users`).Scan(&users); err != nil {
		return 0, 0, fmt.Errorf("postgres: count scim users: %w", err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM scim_groups`).Scan(&groups); err != nil {
		return 0, 0, fmt.Errorf("postgres: count scim groups: %w", err)
	}
	return users, groups, nil
}

// Compile-time interface satisfaction — matches the SQLite
// driver's assertion.
var _ scim.Store = (*SCIMStore)(nil)

// -- helpers --

func replaceMemberships(ctx context.Context, tx *sql.Tx, groupID string, members []scim.Ref) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM scim_group_members WHERE group_id = $1`, groupID); err != nil {
		return fmt.Errorf("postgres: clear scim members: %w", err)
	}
	if len(members) == 0 {
		return nil
	}
	// ON CONFLICT DO NOTHING matches the SQLite driver's
	// INSERT OR IGNORE — duplicate member entries in the input
	// list should not fail the whole ReplaceGroup call.
	const insertQ = `
INSERT INTO scim_group_members (group_id, user_id)
VALUES ($1, $2)
ON CONFLICT (group_id, user_id) DO NOTHING
`
	for _, m := range members {
		if m.Value == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, insertQ, groupID, m.Value); err != nil {
			return fmt.Errorf("postgres: insert scim member: %w", err)
		}
	}
	return nil
}

func unmarshalUser(body []byte) (scim.User, error) {
	var u scim.User
	if err := json.Unmarshal(body, &u); err != nil {
		return scim.User{}, fmt.Errorf("postgres: unmarshal scim user: %w", err)
	}
	return u, nil
}

func unmarshalGroup(body []byte) (scim.Group, error) {
	var g scim.Group
	if err := json.Unmarshal(body, &g); err != nil {
		return scim.Group{}, fmt.Errorf("postgres: unmarshal scim group: %w", err)
	}
	return g, nil
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// isUniqueViolation reports whether err is a Postgres unique-
// constraint violation (SQLSTATE 23505). Pgx wraps the wire-
// level error in *pgconn.PgError; errors.As unpacks it whether
// or not intermediate wrappers are present.
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "23505"
	}
	return false
}
