package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
	"github.com/sebastienrousseau/rousseau-agent/internal/state"
)

// newMockStore returns a Store wired to a sqlmock-backed *sql.DB plus
// the mock controller. The unexported db field is reachable because
// this test lives in package postgres. Ping monitoring is enabled so
// Open's PingContext guard is programmable.
func newMockStore(t *testing.T) (*Store, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New(sqlmock.MonitorPingsOption(true))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() }) //nolint:errcheck // best-effort cleanup; the test may already have closed it
	return &Store{db: db}, mock
}

// q escapes a literal SQL string for the default regexp query matcher
// so expectations assert the EXACT query text store.go issues.
func q(s string) string { return regexp.QuoteMeta(s) }

// -- Open: seam-driven ping / schema branches ----------------------

func TestOpen_OpenDBError(t *testing.T) {
	orig := openDB
	t.Cleanup(func() { openDB = orig })
	openDB = func(string, string) (*sql.DB, error) {
		return nil, errors.New("driver boom")
	}
	_, err := Open(context.Background(), "postgres://x")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "postgres: open:")
}

func TestOpen_PingFailureClosesAndWraps(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.MonitorPingsOption(true))
	require.NoError(t, err)
	mock.ExpectPing().WillReturnError(errors.New("no pong"))
	mock.ExpectClose()

	orig := openDB
	t.Cleanup(func() { openDB = orig })
	openDB = func(string, string) (*sql.DB, error) { return db, nil }

	_, err = Open(context.Background(), "postgres://x")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "postgres: ping:")
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestOpen_SchemaApplyFailureClosesAndWraps(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.MonitorPingsOption(true))
	require.NoError(t, err)
	mock.ExpectPing()
	mock.ExpectExec(".*").WillReturnError(errors.New("schema boom"))
	mock.ExpectClose()

	orig := openDB
	t.Cleanup(func() { openDB = orig })
	openDB = func(string, string) (*sql.DB, error) { return db, nil }

	_, err = Open(context.Background(), "postgres://x")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "postgres: apply schema:")
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestOpen_HappyPathAppliesSchema(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.MonitorPingsOption(true))
	require.NoError(t, err)
	mock.ExpectPing()
	// Open now runs three Execs: schema, ADD COLUMN IF NOT EXISTS
	// (sender), CREATE INDEX IF NOT EXISTS. Match each with the
	// permissive ".*" pattern the original test used — we're not
	// testing the exact DDL here, just that Open runs to completion.
	mock.ExpectExec(".*").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(".*").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(".*").WillReturnResult(sqlmock.NewResult(0, 0))

	orig := openDB
	t.Cleanup(func() { openDB = orig })
	openDB = func(string, string) (*sql.DB, error) { return db, nil }

	store, err := Open(context.Background(), "postgres://x")
	require.NoError(t, err)
	require.NotNil(t, store)
	assert.NoError(t, mock.ExpectationsWereMet())
}

// -- Save ----------------------------------------------------------

const saveQuery = `
INSERT INTO sessions (id, title, payload, message_count, created_at, updated_at, sender)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (id) DO UPDATE SET
    title = EXCLUDED.title,
    payload = EXCLUDED.payload,
    message_count = EXCLUDED.message_count,
    updated_at = EXCLUDED.updated_at,
    sender = EXCLUDED.sender
`

func TestSave_HappyPath(t *testing.T) {
	store, mock := newMockStore(t)
	sess := agent.NewSession("hello")
	sess.Append(agent.NewUserText("hi"))

	mock.ExpectExec(q(saveQuery)).
		WithArgs(sess.ID, sess.Title, sqlmock.AnyArg(), 1, sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))

	require.NoError(t, store.Save(context.Background(), sess))
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestSave_ExecError(t *testing.T) {
	store, mock := newMockStore(t)
	sess := agent.NewSession("hello")

	mock.ExpectExec(q(saveQuery)).WillReturnError(errors.New("exec boom"))

	err := store.Save(context.Background(), sess)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "postgres: save session:")
	assert.NoError(t, mock.ExpectationsWereMet())
}

// -- Load ----------------------------------------------------------

const loadQuery = `SELECT payload FROM sessions WHERE id = $1`

func TestLoad_HappyPath(t *testing.T) {
	store, mock := newMockStore(t)
	want := agent.NewSession("loaded")
	want.Append(agent.NewUserText("body"))
	payload, err := json.Marshal(want)
	require.NoError(t, err)

	mock.ExpectQuery(q(loadQuery)).
		WithArgs(want.ID).
		WillReturnRows(sqlmock.NewRows([]string{"payload"}).AddRow(string(payload)))

	got, err := store.Load(context.Background(), want.ID)
	require.NoError(t, err)
	assert.Equal(t, want.ID, got.ID)
	assert.Equal(t, "loaded", got.Title)
	require.Len(t, got.Messages, 1)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestLoad_NoRowsReturnsErrNotFound(t *testing.T) {
	store, mock := newMockStore(t)
	mock.ExpectQuery(q(loadQuery)).
		WithArgs("missing").
		WillReturnError(sql.ErrNoRows)

	_, err := store.Load(context.Background(), "missing")
	assert.ErrorIs(t, err, state.ErrNotFound)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestLoad_QueryError(t *testing.T) {
	store, mock := newMockStore(t)
	mock.ExpectQuery(q(loadQuery)).
		WithArgs("x").
		WillReturnError(errors.New("db down"))

	_, err := store.Load(context.Background(), "x")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "postgres: load session:")
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestLoad_UnmarshalError(t *testing.T) {
	store, mock := newMockStore(t)
	mock.ExpectQuery(q(loadQuery)).
		WithArgs("x").
		WillReturnRows(sqlmock.NewRows([]string{"payload"}).AddRow("{not-json"))

	_, err := store.Load(context.Background(), "x")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "postgres: unmarshal session:")
	assert.NoError(t, mock.ExpectationsWereMet())
}

// -- List ----------------------------------------------------------

const (
	listQuery      = `SELECT id, title, message_count, updated_at FROM sessions ORDER BY updated_at DESC`
	listQueryLimit = listQuery + ` LIMIT $1`
)

func TestList_HappyPathNoLimit(t *testing.T) {
	store, mock := newMockStore(t)
	rows := sqlmock.NewRows([]string{"id", "title", "message_count", "updated_at"}).
		AddRow("id-1", "t1", 2, timeValue()).
		AddRow("id-2", "t2", 0, timeValue())
	mock.ExpectQuery(q(listQuery)).WillReturnRows(rows)

	out, err := store.List(context.Background(), 0)
	require.NoError(t, err)
	require.Len(t, out, 2)
	assert.Equal(t, "id-1", out[0].ID)
	assert.NotEmpty(t, out[0].UpdatedAt)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestList_HappyPathWithLimit(t *testing.T) {
	store, mock := newMockStore(t)
	rows := sqlmock.NewRows([]string{"id", "title", "message_count", "updated_at"}).
		AddRow("id-1", "t1", 1, timeValue())
	mock.ExpectQuery(q(listQueryLimit)).WithArgs(5).WillReturnRows(rows)

	out, err := store.List(context.Background(), 5)
	require.NoError(t, err)
	require.Len(t, out, 1)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestList_QueryError(t *testing.T) {
	store, mock := newMockStore(t)
	mock.ExpectQuery(q(listQuery)).WillReturnError(errors.New("list boom"))

	_, err := store.List(context.Background(), 0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "postgres: list sessions:")
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestList_ScanError(t *testing.T) {
	store, mock := newMockStore(t)
	// A non-time value in the updated_at column fails Scan into time.Time.
	rows := sqlmock.NewRows([]string{"id", "title", "message_count", "updated_at"}).
		AddRow("id-1", "t1", 1, "not-a-timestamp")
	mock.ExpectQuery(q(listQuery)).WillReturnRows(rows)

	_, err := store.List(context.Background(), 0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "postgres: scan summary:")
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestList_RowsIterationError(t *testing.T) {
	store, mock := newMockStore(t)
	rows := sqlmock.NewRows([]string{"id", "title", "message_count", "updated_at"}).
		AddRow("id-1", "t1", 1, timeValue()).
		RowError(1, errors.New("iterate boom"))
	// Second row triggers the error surfaced by rows.Err().
	rows.AddRow("id-2", "t2", 1, timeValue())
	mock.ExpectQuery(q(listQuery)).WillReturnRows(rows)

	_, err := store.List(context.Background(), 0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "postgres: iterate summaries:")
	assert.NoError(t, mock.ExpectationsWereMet())
}

// -- Delete --------------------------------------------------------

const deleteQuery = `DELETE FROM sessions WHERE id = $1`

func TestDelete_HappyPath(t *testing.T) {
	store, mock := newMockStore(t)
	mock.ExpectExec(q(deleteQuery)).
		WithArgs("id-1").
		WillReturnResult(sqlmock.NewResult(0, 1))

	require.NoError(t, store.Delete(context.Background(), "id-1"))
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestDelete_MissingIsNoop(t *testing.T) {
	// Deleting a missing row (0 rows affected) is not an error —
	// matches the SQLite driver's idempotent-cleanup contract.
	store, mock := newMockStore(t)
	mock.ExpectExec(q(deleteQuery)).
		WithArgs("gone").
		WillReturnResult(sqlmock.NewResult(0, 0))

	require.NoError(t, store.Delete(context.Background(), "gone"))
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestDelete_ExecError(t *testing.T) {
	store, mock := newMockStore(t)
	mock.ExpectExec(q(deleteQuery)).
		WithArgs("id-1").
		WillReturnError(errors.New("delete boom"))

	err := store.Delete(context.Background(), "id-1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "postgres: delete session:")
	assert.NoError(t, mock.ExpectationsWereMet())
}

// -- Close ---------------------------------------------------------

func TestClose_ReleasesPool(t *testing.T) {
	store, mock := newMockStore(t)
	mock.ExpectClose()
	require.NoError(t, store.Close())
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestClose_PropagatesError(t *testing.T) {
	store, mock := newMockStore(t)
	mock.ExpectClose().WillReturnError(errors.New("close boom"))
	err := store.Close()
	require.Error(t, err)
	assert.NoError(t, mock.ExpectationsWereMet())
}

// timeValue returns a fixed timestamp as a driver-native time.Time so
// database/sql scans it straight into the *time.Time target.
func timeValue() time.Time {
	return time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
}
