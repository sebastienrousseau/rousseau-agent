package storetest_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/state"
	"github.com/sebastienrousseau/rousseau-agent/internal/state/sqlite"
	"github.com/sebastienrousseau/rousseau-agent/internal/state/storetest"
)

// The suite proves itself against the reference driver so a change
// to a contract case is caught here before the driver packages run.
func TestRun_AgainstReferenceDriver(t *testing.T) {
	storetest.Run(t, func(t *testing.T) state.Store {
		s, err := sqlite.Open(context.Background(), ":memory:")
		require.NoError(t, err)
		return s
	})
}
