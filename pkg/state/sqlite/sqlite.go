// Package sqlite re-exports the internal/state/sqlite store surface
// so external modules can open a rousseau-compatible session store
// without importing /internal.
package sqlite

import (
	"github.com/sebastienrousseau/rousseau-agent/internal/state/sqlite"
)

// Store aliases [sqlite.Store].
type Store = sqlite.Store

// JIDMap aliases [sqlite.JIDMap].
type JIDMap = sqlite.JIDMap

// CronStore aliases [sqlite.CronStore].
type CronStore = sqlite.CronStore

// OAuthTokens aliases [sqlite.OAuthTokens].
type OAuthTokens = sqlite.OAuthTokens

// RecallVectors aliases [sqlite.RecallVectors].
type RecallVectors = sqlite.RecallVectors

// Direct function aliases.
var (
	Open              = sqlite.Open
	NewJIDMap         = sqlite.NewJIDMap
	NewCronStore      = sqlite.NewCronStore
	NewOAuthTokens    = sqlite.NewOAuthTokens
	NewRecallVectors  = sqlite.NewRecallVectors
	NewRecallSearcher = sqlite.NewRecallSearcher
)
