package cli

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/sebastienrousseau/rousseau-agent/internal/config"
	"github.com/sebastienrousseau/rousseau-agent/internal/state"
	pgstore "github.com/sebastienrousseau/rousseau-agent/internal/state/postgres"
	sqlitestore "github.com/sebastienrousseau/rousseau-agent/internal/state/sqlite"
)

// SearchableStore is the read-side surface every command that
// exposes full-text search consumes (rousseau session list /
// search / show, rousseau mcp). Both drivers satisfy it because
// they share the underlying method signatures — SQLite's FTS5
// virtual table and Postgres's tsvector generated column both
// front the same Search(ctx, query, opts) contract.
//
// Kept at the consumer end (cli/) per Go idiom — the state
// package doesn't need to know CLI wants these together.
type SearchableStore interface {
	state.Store
	Search(ctx context.Context, query string, opts sqlitestore.SearchOptions) ([]sqlitestore.SearchHit, error)
	EnsureSearch(ctx context.Context) error
}

// CronStoreI is the cron-scheduler surface both drivers expose.
// Method signatures were designed identical during the §2.4a
// port; the CronJob type is aliased to sqlitestore.CronJob in
// the Postgres driver so both concrete types plug in
// straightforwardly.
type CronStoreI interface {
	Put(ctx context.Context, j sqlitestore.CronJob) error
	List(ctx context.Context) ([]sqlitestore.CronJob, error)
	Delete(ctx context.Context, id string) error
	SetEnabled(ctx context.Context, id string, enabled bool) error
	RecordRun(ctx context.Context, id string, at time.Time) error
}

// SessionCostStoreI is the cost-recording surface used by
// `rousseau session cost`. Both drivers ship matching signatures;
// the CostRecord / Summary / SessionRollup shapes are aliased in
// the Postgres driver so callers pass the same values across
// backends.
type SessionCostStoreI interface {
	Record(ctx context.Context, r sqlitestore.CostRecord) error
	SumBySession(ctx context.Context, sessionID string, since time.Duration) (sqlitestore.Summary, error)
	TopSessions(ctx context.Context, since time.Duration, limit int) ([]sqlitestore.SessionRollup, error)
}

// openSearchableStore opens the base state Store and returns it
// as a SearchableStore, dispatching on cfg.Driver. Both drivers
// install their FTS surface via EnsureSearch on first open — the
// helper does that here so callers don't have to remember.
//
// This is the driver-agnostic replacement for openSQLiteStore
// for the read-side commands (session, mcp). The daemon still
// uses openSQLiteStore because its extension-hungry constructors
// (identity, jidmap, claude cache, audit chain, scim) are not
// yet driver-agnostic — that is the next follow-up.
func openSearchableStore(ctx context.Context, cfg config.StateConfig) (SearchableStore, error) {
	store, err := openStore(ctx, cfg)
	if err != nil {
		return nil, err
	}
	// The state.Store interface does not surface Search /
	// EnsureSearch, so we upcast to a driver-native concrete
	// type. Both drivers satisfy SearchableStore — the assertion
	// is safe as long as openStore only returns those two.
	ss, ok := store.(SearchableStore)
	if !ok {
		_ = store.Close() //nolint:errcheck // cleanup on error path
		return nil, fmt.Errorf("cli: driver %q does not implement SearchableStore", driverName(cfg))
	}
	if err := ss.EnsureSearch(ctx); err != nil {
		_ = store.Close() //nolint:errcheck // cleanup on error path
		return nil, fmt.Errorf("cli: ensure search: %w", err)
	}
	return ss, nil
}

// openCronStore constructs a CronStoreI on top of an already-open
// SearchableStore, dispatching on the driver's concrete type.
// The two branches call each driver's respective constructor,
// keeping the driver-specific type only at the assembly site.
func openCronStore(ctx context.Context, store SearchableStore) (CronStoreI, error) {
	switch s := store.(type) {
	case *sqlitestore.Store:
		return sqlitestore.NewCronStore(ctx, s)
	case *pgstore.Store:
		return pgstore.NewCronStore(ctx, s)
	default:
		return nil, errors.New("cli: unknown store type for cron")
	}
}

// openSessionCostStore constructs a SessionCostStoreI on top of
// an already-open SearchableStore, dispatching on the driver's
// concrete type — same pattern as openCronStore.
func openSessionCostStore(ctx context.Context, store SearchableStore) (SessionCostStoreI, error) {
	switch s := store.(type) {
	case *sqlitestore.Store:
		return sqlitestore.NewSessionCostStore(ctx, s)
	case *pgstore.Store:
		return pgstore.NewSessionCostStore(ctx, s)
	default:
		return nil, errors.New("cli: unknown store type for session costs")
	}
}

// driverName returns a friendly name for diagnostics. Small
// wrapper so callers don't repeat the empty-defaults-to-sqlite
// logic.
func driverName(cfg config.StateConfig) string {
	if cfg.Driver == "" {
		return "sqlite"
	}
	return cfg.Driver
}
