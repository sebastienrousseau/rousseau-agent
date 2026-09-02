package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/sebastienrousseau/rousseau-agent/internal/auth/scim"
)

// scimSchema stores SCIM-provisioned users + groups.
//
// Design choices:
//
//   - IDs are UUIDs assigned by the daemon (SCIM 2.0 says the
//     SP owns the ID; the IdP correlates via externalId).
//   - Bodies are JSON blobs so schema evolution (extension
//     attributes, custom fields) doesn't require migrations.
//     Load-bearing columns (userName, displayName,
//     externalId) are lifted for indexed lookup.
//   - Group membership lives in a join table so a lookup
//     "which groups is user X in" is one query, not a
//     JSON-parse loop.
const scimSchema = `
CREATE TABLE IF NOT EXISTS scim_users (
    id           TEXT PRIMARY KEY,
    user_name    TEXT NOT NULL UNIQUE,
    external_id  TEXT UNIQUE,
    body         TEXT NOT NULL,
    created_at   TEXT NOT NULL,
    updated_at   TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS scim_groups (
    id           TEXT PRIMARY KEY,
    display_name TEXT NOT NULL UNIQUE,
    external_id  TEXT UNIQUE,
    body         TEXT NOT NULL,
    created_at   TEXT NOT NULL,
    updated_at   TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS scim_group_members (
    group_id TEXT NOT NULL,
    user_id  TEXT NOT NULL,
    PRIMARY KEY (group_id, user_id),
    FOREIGN KEY (group_id) REFERENCES scim_groups(id) ON DELETE CASCADE,
    FOREIGN KEY (user_id)  REFERENCES scim_users(id)  ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_scim_group_members_user
    ON scim_group_members(user_id);
`

// SCIMStore is a SQLite-backed [scim.Store].
type SCIMStore struct {
	db *sql.DB
}

// NewSCIMStore returns a store that shares s's SQLite database.
func NewSCIMStore(ctx context.Context, s *Store) (*SCIMStore, error) {
	if _, err := s.db.ExecContext(ctx, scimSchema); err != nil {
		return nil, fmt.Errorf("sqlite: apply scim schema: %w", err)
	}
	return &SCIMStore{db: s.db}, nil
}

// -- User operations --

// CreateUser satisfies [scim.Store]. Populates the SCIM ID
// (UUID v4) if the caller didn't supply one.
func (s *SCIMStore) CreateUser(ctx context.Context, u scim.User) (scim.User, error) {
	if u.ID == "" {
		u.ID = uuid.NewString()
	}
	u.Active = true // default per SCIM 2.0 when omitted
	body, err := json.Marshal(u)
	if err != nil {
		return scim.User{}, fmt.Errorf("sqlite: marshal scim user: %w", err)
	}
	now := isoNowUTC()
	const q = `
INSERT INTO scim_users (id, user_name, external_id, body, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?)
`
	if _, err := s.db.ExecContext(ctx, q, u.ID, u.UserName, nullIfEmpty(u.ExternalID), string(body), now, now); err != nil {
		if isUniqueViolation(err) {
			return scim.User{}, fmt.Errorf("%w: userName or externalId already exists", scim.ErrConflict)
		}
		return scim.User{}, fmt.Errorf("sqlite: create scim user: %w", err)
	}
	return u, nil
}

// GetUser satisfies [scim.Store].
func (s *SCIMStore) GetUser(ctx context.Context, id string) (scim.User, error) {
	const q = `SELECT body FROM scim_users WHERE id = ?`
	var body string
	err := s.db.QueryRowContext(ctx, q, id).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return scim.User{}, scim.ErrNotFound
	}
	if err != nil {
		return scim.User{}, fmt.Errorf("sqlite: get scim user: %w", err)
	}
	return unmarshalUser(body)
}

// ListUsers satisfies [scim.Store].
func (s *SCIMStore) ListUsers(ctx context.Context, filterUserName string, startIndex, count int) ([]scim.User, int, error) {
	countQuery := `SELECT COUNT(*) FROM scim_users`
	listQuery := `SELECT body FROM scim_users`
	args := []any{}
	if filterUserName != "" {
		countQuery += ` WHERE user_name = ?`
		listQuery += ` WHERE user_name = ?`
		args = append(args, filterUserName)
	}
	listQuery += ` ORDER BY user_name`
	// Pagination — SCIM startIndex is 1-based.
	if count > 0 {
		listQuery += ` LIMIT ? OFFSET ?`
		offset := startIndex - 1
		if offset < 0 {
			offset = 0
		}
		args = append(args, count, offset)
	}

	var total int
	countArgs := args
	if count > 0 {
		countArgs = args[:len(args)-2]
	}
	if err := s.db.QueryRowContext(ctx, countQuery, countArgs...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("sqlite: count scim users: %w", err)
	}

	rows, err := s.db.QueryContext(ctx, listQuery, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("sqlite: list scim users: %w", err)
	}
	defer func() { _ = rows.Close() }() //nolint:errcheck // best-effort cleanup

	var out []scim.User
	for rows.Next() {
		var body string
		if err := rows.Scan(&body); err != nil {
			return nil, 0, fmt.Errorf("sqlite: scan scim user: %w", err)
		}
		u, err := unmarshalUser(body)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, u)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("sqlite: iterate scim users: %w", err)
	}
	return out, total, nil
}

// ReplaceUser satisfies [scim.Store].
func (s *SCIMStore) ReplaceUser(ctx context.Context, id string, u scim.User) (scim.User, error) {
	// Confirm existence first for a clean ErrNotFound.
	if _, err := s.GetUser(ctx, id); err != nil {
		return scim.User{}, err
	}
	u.ID = id
	body, err := json.Marshal(u)
	if err != nil {
		return scim.User{}, fmt.Errorf("sqlite: marshal scim user: %w", err)
	}
	now := isoNowUTC()
	const q = `
UPDATE scim_users
SET user_name = ?, external_id = ?, body = ?, updated_at = ?
WHERE id = ?
`
	if _, err := s.db.ExecContext(ctx, q, u.UserName, nullIfEmpty(u.ExternalID), string(body), now, id); err != nil {
		if isUniqueViolation(err) {
			return scim.User{}, fmt.Errorf("%w: userName or externalId collision", scim.ErrConflict)
		}
		return scim.User{}, fmt.Errorf("sqlite: replace scim user: %w", err)
	}
	return u, nil
}

// DeleteUser satisfies [scim.Store]. Idempotent — missing ID
// returns nil (matches SCIM 2.0 §3.6).
func (s *SCIMStore) DeleteUser(ctx context.Context, id string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM scim_users WHERE id = ?`, id); err != nil {
		return fmt.Errorf("sqlite: delete scim user: %w", err)
	}
	return nil
}

// -- Group operations --

// CreateGroup satisfies [scim.Store]. Members reference user
// IDs — the store persists the reference list but doesn't
// enforce referential integrity beyond the foreign key
// declared in schema.
func (s *SCIMStore) CreateGroup(ctx context.Context, g scim.Group) (scim.Group, error) {
	if g.ID == "" {
		g.ID = uuid.NewString()
	}
	body, err := json.Marshal(g)
	if err != nil {
		return scim.Group{}, fmt.Errorf("sqlite: marshal scim group: %w", err)
	}
	now := isoNowUTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return scim.Group{}, fmt.Errorf("sqlite: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }() //nolint:errcheck // rollback is idempotent on committed tx

	const insertQ = `
INSERT INTO scim_groups (id, display_name, external_id, body, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?)
`
	if _, err := tx.ExecContext(ctx, insertQ, g.ID, g.DisplayName, nullIfEmpty(g.ExternalID), string(body), now, now); err != nil {
		if isUniqueViolation(err) {
			return scim.Group{}, fmt.Errorf("%w: displayName or externalId already exists", scim.ErrConflict)
		}
		return scim.Group{}, fmt.Errorf("sqlite: create scim group: %w", err)
	}
	if err := replaceMemberships(ctx, tx, g.ID, g.Members); err != nil {
		return scim.Group{}, err
	}
	if err := tx.Commit(); err != nil {
		return scim.Group{}, fmt.Errorf("sqlite: commit create scim group: %w", err)
	}
	return g, nil
}

// GetGroup satisfies [scim.Store].
func (s *SCIMStore) GetGroup(ctx context.Context, id string) (scim.Group, error) {
	const q = `SELECT body FROM scim_groups WHERE id = ?`
	var body string
	err := s.db.QueryRowContext(ctx, q, id).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return scim.Group{}, scim.ErrNotFound
	}
	if err != nil {
		return scim.Group{}, fmt.Errorf("sqlite: get scim group: %w", err)
	}
	return unmarshalGroup(body)
}

// ListGroups satisfies [scim.Store].
func (s *SCIMStore) ListGroups(ctx context.Context, filterDisplayName string, startIndex, count int) ([]scim.Group, int, error) {
	countQuery := `SELECT COUNT(*) FROM scim_groups`
	listQuery := `SELECT body FROM scim_groups`
	args := []any{}
	if filterDisplayName != "" {
		countQuery += ` WHERE display_name = ?`
		listQuery += ` WHERE display_name = ?`
		args = append(args, filterDisplayName)
	}
	listQuery += ` ORDER BY display_name`
	if count > 0 {
		listQuery += ` LIMIT ? OFFSET ?`
		offset := startIndex - 1
		if offset < 0 {
			offset = 0
		}
		args = append(args, count, offset)
	}

	var total int
	countArgs := args
	if count > 0 {
		countArgs = args[:len(args)-2]
	}
	if err := s.db.QueryRowContext(ctx, countQuery, countArgs...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("sqlite: count scim groups: %w", err)
	}

	rows, err := s.db.QueryContext(ctx, listQuery, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("sqlite: list scim groups: %w", err)
	}
	defer func() { _ = rows.Close() }() //nolint:errcheck // best-effort cleanup

	var out []scim.Group
	for rows.Next() {
		var body string
		if err := rows.Scan(&body); err != nil {
			return nil, 0, fmt.Errorf("sqlite: scan scim group: %w", err)
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
		return scim.Group{}, fmt.Errorf("sqlite: marshal scim group: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return scim.Group{}, fmt.Errorf("sqlite: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }() //nolint:errcheck // idempotent
	const q = `
UPDATE scim_groups
SET display_name = ?, external_id = ?, body = ?, updated_at = ?
WHERE id = ?
`
	if _, err := tx.ExecContext(ctx, q, g.DisplayName, nullIfEmpty(g.ExternalID), string(body), isoNowUTC(), id); err != nil {
		if isUniqueViolation(err) {
			return scim.Group{}, fmt.Errorf("%w: displayName or externalId collision", scim.ErrConflict)
		}
		return scim.Group{}, fmt.Errorf("sqlite: replace scim group: %w", err)
	}
	if err := replaceMemberships(ctx, tx, id, g.Members); err != nil {
		return scim.Group{}, err
	}
	if err := tx.Commit(); err != nil {
		return scim.Group{}, fmt.Errorf("sqlite: commit replace scim group: %w", err)
	}
	return g, nil
}

// DeleteGroup satisfies [scim.Store]. Idempotent.
func (s *SCIMStore) DeleteGroup(ctx context.Context, id string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM scim_groups WHERE id = ?`, id); err != nil {
		return fmt.Errorf("sqlite: delete scim group: %w", err)
	}
	return nil
}

// -- Cross-cutting --

// LookupUserByExternalID satisfies [scim.Store].
func (s *SCIMStore) LookupUserByExternalID(ctx context.Context, externalID string) (scim.User, error) {
	if externalID == "" {
		return scim.User{}, scim.ErrNotFound
	}
	const q = `SELECT body FROM scim_users WHERE external_id = ?`
	var body string
	err := s.db.QueryRowContext(ctx, q, externalID).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return scim.User{}, scim.ErrNotFound
	}
	if err != nil {
		return scim.User{}, fmt.Errorf("sqlite: lookup scim user by externalId: %w", err)
	}
	return unmarshalUser(body)
}

// UserGroupNames returns the displayNames of every group the
// user belongs to, sorted alphabetically. Returns nil when the
// user has no memberships or doesn't exist — same shape either
// way, matches what downstream RBAC / OPA policies expect.
//
// Not part of the [scim.Store] interface (that surface is
// pinned to what the HTTP handlers use); exposed here for the
// SSO ResolveTransportID adapter in cli/sso_wire.go.
func (s *SCIMStore) UserGroupNames(ctx context.Context, userID string) ([]string, error) {
	if userID == "" {
		return nil, nil
	}
	const q = `
SELECT g.display_name
FROM scim_groups g
JOIN scim_group_members m ON m.group_id = g.id
WHERE m.user_id = ?
ORDER BY g.display_name
`
	rows, err := s.db.QueryContext(ctx, q, userID)
	if err != nil {
		return nil, fmt.Errorf("sqlite: user group names: %w", err)
	}
	defer func() { _ = rows.Close() }() //nolint:errcheck // best-effort cleanup

	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("sqlite: scan group name: %w", err)
		}
		out = append(out, name)
	}
	return out, nil
}

// Count satisfies [scim.Store]. Used by doctor.
func (s *SCIMStore) Count(ctx context.Context) (int, int, error) {
	var users, groups int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM scim_users`).Scan(&users); err != nil {
		return 0, 0, fmt.Errorf("sqlite: count scim users: %w", err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM scim_groups`).Scan(&groups); err != nil {
		return 0, 0, fmt.Errorf("sqlite: count scim groups: %w", err)
	}
	return users, groups, nil
}

// Compile-time interface satisfaction.
var _ scim.Store = (*SCIMStore)(nil)

// -- helpers --

func replaceMemberships(ctx context.Context, tx *sql.Tx, groupID string, members []scim.Ref) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM scim_group_members WHERE group_id = ?`, groupID); err != nil {
		return fmt.Errorf("sqlite: clear scim members: %w", err)
	}
	if len(members) == 0 {
		return nil
	}
	const insertQ = `INSERT OR IGNORE INTO scim_group_members (group_id, user_id) VALUES (?, ?)`
	for _, m := range members {
		if m.Value == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, insertQ, groupID, m.Value); err != nil {
			return fmt.Errorf("sqlite: insert scim member: %w", err)
		}
	}
	return nil
}

func unmarshalUser(body string) (scim.User, error) {
	var u scim.User
	if err := json.Unmarshal([]byte(body), &u); err != nil {
		return scim.User{}, fmt.Errorf("sqlite: unmarshal scim user: %w", err)
	}
	return u, nil
}

func unmarshalGroup(body string) (scim.Group, error) {
	var g scim.Group
	if err := json.Unmarshal([]byte(body), &g); err != nil {
		return scim.Group{}, fmt.Errorf("sqlite: unmarshal scim group: %w", err)
	}
	return g, nil
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// isoNowUTC produces the timestamp format the rest of the
// sqlite package uses.
func isoNowUTC() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
}

// isUniqueViolation reports whether err is the SQLite unique-
// constraint failure. The modernc driver wraps these in an
// error whose Error() text contains "UNIQUE constraint failed".
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	return contains(err.Error(), "UNIQUE constraint failed")
}

// contains is a tiny local helper so we don't drag `strings`
// into this file just for one check.
func contains(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
