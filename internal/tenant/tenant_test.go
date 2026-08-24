package tenant_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/sebastienrousseau/rousseau-agent/internal/tenant"
)

func TestNewMapResolver_ReturnsScaffold(t *testing.T) {
	_, err := tenant.NewMapResolver(nil)
	assert.ErrorIs(t, err, tenant.ErrScaffold)
}

func TestWithID_FromContext_RoundTrip(t *testing.T) {
	ctx := tenant.WithID(context.Background(), "team-a")
	assert.Equal(t, tenant.ID("team-a"), tenant.FromContext(ctx))
}

func TestFromContext_EmptyWhenUnset(t *testing.T) {
	assert.Equal(t, tenant.ID(""), tenant.FromContext(context.Background()))
}

func TestWithID_EmptyIsNoop(t *testing.T) {
	// Setting empty should leave a pre-existing value intact.
	base := tenant.WithID(context.Background(), "existing")
	after := tenant.WithID(base, "")
	assert.Equal(t, tenant.ID("existing"), tenant.FromContext(after))
}
