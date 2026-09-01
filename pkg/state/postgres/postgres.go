// Package postgres re-exports the internal/state/postgres store
// surface so external modules can open a rousseau-compatible
// Postgres-backed session store without importing /internal.
package postgres

import (
	"github.com/sebastienrousseau/rousseau-agent/internal/state/postgres"
)

// Store aliases [postgres.Store].
type Store = postgres.Store

// Open aliases [postgres.Open].
var Open = postgres.Open
