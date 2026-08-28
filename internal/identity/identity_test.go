package identity_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/sebastienrousseau/rousseau-agent/internal/identity"
)

// The package is thin — just interface types + sentinel errors.
// These tests pin the sentinel identities so downstream errors.Is
// comparisons remain stable.

func TestErrNotLinkedMessage(t *testing.T) {
	assert.Equal(t, "identity: handle not linked to any identity", identity.ErrNotLinked.Error())
	assert.True(t, errors.Is(identity.ErrNotLinked, identity.ErrNotLinked))
}

func TestErrAlreadyLinkedMessage(t *testing.T) {
	assert.Equal(t, "identity: handle already linked", identity.ErrAlreadyLinked.Error())
}

func TestErrIdentityNotFoundMessage(t *testing.T) {
	assert.Equal(t, "identity: not found", identity.ErrIdentityNotFound.Error())
}

func TestID_Assignable(t *testing.T) {
	// Ensures ID is a string-alias suitable for map keys + string ops.
	var id identity.ID = "id-abc"
	assert.Equal(t, "id-abc", string(id))
}

func TestHandle_ZeroValueOK(t *testing.T) {
	// Handle should be safe to construct as a zero value (used in
	// Handles slices before backfilling).
	var h identity.Handle
	assert.Equal(t, "", h.Transport)
	assert.Equal(t, "", h.Sender)
	assert.True(t, h.VerifiedAt.IsZero())
}
