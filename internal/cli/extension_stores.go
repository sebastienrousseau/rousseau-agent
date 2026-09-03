package cli

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/sebastienrousseau/rousseau-agent/internal/auth/scim"
	"github.com/sebastienrousseau/rousseau-agent/internal/auth/sso"
	"github.com/sebastienrousseau/rousseau-agent/internal/config"
	"github.com/sebastienrousseau/rousseau-agent/internal/identity"
	"github.com/sebastienrousseau/rousseau-agent/internal/observability/audit_egress"
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
	SearchBySender(ctx context.Context, sender, query string, opts sqlitestore.SearchOptions) ([]sqlitestore.SearchHit, error)
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
// Used by every rousseau command that touches state (session,
// mcp, cron, daemon). Combined with the openXxxStore extension
// factories below, this is what lets the whole daemon accept
// either driver end-to-end — the openSQLiteStore fail-CLOSED
// gate that predated this seam has been retired.
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

// JIDMapI is the narrow surface the transport router consumes to
// map (transport, sender) → session ID. Both drivers implement
// the same three-method surface with the same signatures.
type JIDMapI interface {
	Get(ctx context.Context, jid string) (sessionID string, ok bool, err error)
	Put(ctx context.Context, jid, sessionID string) error
}

// ClaudeSessionCacheI is the narrow surface the claudecli
// provider consumes to remember Claude Code session IDs across
// daemon restarts. Both drivers ship matching IsKnown / Remember
// signatures.
type ClaudeSessionCacheI interface {
	IsKnown(id string) bool
	Remember(id string)
}

// SCIMGroupNamesStore is the SSO-adapter surface (looks up group
// display-names for a SCIM user ID). Both drivers expose the
// method with the same signature — kept as a separate interface
// from scim.Store because the SCIM HTTP handler doesn't need it
// and threading a wider interface through cli/sso_scim_adapter.go
// would force test doubles to implement CRUD they never touch.
type SCIMGroupNamesStore interface {
	UserGroupNames(ctx context.Context, userID string) ([]string, error)
}

// openIdentityStore constructs a driver-appropriate
// identity.Resolver on top of an already-open SearchableStore.
// Same dispatch pattern as openCronStore / openSessionCostStore.
func openIdentityStore(ctx context.Context, store SearchableStore) (identity.Resolver, error) {
	switch s := store.(type) {
	case *sqlitestore.Store:
		return sqlitestore.NewIdentityStore(ctx, s)
	case *pgstore.Store:
		return pgstore.NewIdentityStore(ctx, s)
	default:
		return nil, errors.New("cli: unknown store type for identity")
	}
}

// openJIDMap constructs a driver-appropriate JID → session map
// on top of an already-open SearchableStore.
func openJIDMap(ctx context.Context, store SearchableStore) (JIDMapI, error) {
	switch s := store.(type) {
	case *sqlitestore.Store:
		return sqlitestore.NewJIDMap(ctx, s)
	case *pgstore.Store:
		return pgstore.NewJIDMap(ctx, s)
	default:
		return nil, errors.New("cli: unknown store type for jidmap")
	}
}

// openClaudeSessionCache constructs a driver-appropriate
// Claude-session cache on top of an already-open SearchableStore.
func openClaudeSessionCache(ctx context.Context, store SearchableStore) (ClaudeSessionCacheI, error) {
	switch s := store.(type) {
	case *sqlitestore.Store:
		return sqlitestore.NewClaudeSessionCache(ctx, s)
	case *pgstore.Store:
		return pgstore.NewClaudeSessionCache(ctx, s)
	default:
		return nil, errors.New("cli: unknown store type for claude session cache")
	}
}

// openAuditChainState constructs a driver-appropriate audit
// chain-state store — the ChainedSink loads it at boot and saves
// after every emit.
func openAuditChainState(ctx context.Context, store SearchableStore) (audit_egress.ChainStore, error) {
	switch s := store.(type) {
	case *sqlitestore.Store:
		return sqlitestore.NewAuditChainState(ctx, s)
	case *pgstore.Store:
		return pgstore.NewAuditChainState(ctx, s)
	default:
		return nil, errors.New("cli: unknown store type for audit chain state")
	}
}

// openSCIMStore constructs a driver-appropriate scim.Store on
// top of an already-open SearchableStore. Returned as the shared
// scim.Store interface so the HTTP handler layer doesn't have to
// see driver-specific types.
func openSCIMStore(ctx context.Context, store SearchableStore) (scim.Store, error) {
	switch s := store.(type) {
	case *sqlitestore.Store:
		return sqlitestore.NewSCIMStore(ctx, s)
	case *pgstore.Store:
		return pgstore.NewSCIMStore(ctx, s)
	default:
		return nil, errors.New("cli: unknown store type for scim")
	}
}

// scimGroupNamesFrom returns the group-names accessor for a
// concrete SCIM store. Both drivers expose UserGroupNames with
// the same signature (not on scim.Store), so we re-assert here.
// Returns nil on unknown types — callers treat nil as "no group
// data available" (SSO adapter falls back to token claims).
func scimGroupNamesFrom(s scim.Store) SCIMGroupNamesStore {
	if gs, ok := s.(SCIMGroupNamesStore); ok {
		return gs
	}
	return nil
}

// openSSOBindings constructs a driver-appropriate sso.BindingStore
// on top of an already-open SearchableStore.
func openSSOBindings(ctx context.Context, store SearchableStore) (sso.BindingStore, error) {
	switch s := store.(type) {
	case *sqlitestore.Store:
		return sqlitestore.NewSSOBindings(ctx, s)
	case *pgstore.Store:
		return pgstore.NewSSOBindings(ctx, s)
	default:
		return nil, errors.New("cli: unknown store type for sso bindings")
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
