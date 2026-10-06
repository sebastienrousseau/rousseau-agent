package sqlite

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/state"
	"github.com/sebastienrousseau/rousseau-agent/internal/state/storetest"
)

// The SQLite driver is the reference implementation of the store
// contract; every case in storetest must pass here unconditionally.
func TestStoreContract(t *testing.T) {
	storetest.Run(t, func(t *testing.T) state.Store {
		s, err := Open(context.Background(), ":memory:")
		require.NoError(t, err)
		return s
	})
}
