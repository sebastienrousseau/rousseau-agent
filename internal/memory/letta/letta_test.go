package letta_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/sebastienrousseau/rousseau-agent/internal/memory/letta"
)

func TestNewSQLiteStore_ReturnsScaffold(t *testing.T) {
	_, err := letta.NewSQLiteStore()
	assert.ErrorIs(t, err, letta.ErrScaffold)
}
