package postgres

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/state"
	"github.com/sebastienrousseau/rousseau-agent/internal/state/storetest"
)

// The Postgres driver must pass the same contract as SQLite. Each case
// gets its own schema so cases cannot see each other's rows. Skips
// without ROUSSEAU_TEST_POSTGRES_URL; the Linux CI job sets it.
func TestStoreContract(t *testing.T) {
	requirePG(t)
	storetest.Run(t, func(t *testing.T) state.Store {
		s, err := Open(context.Background(), isolatedDSN(t))
		require.NoError(t, err)
		return s
	})
}
