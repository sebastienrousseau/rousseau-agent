package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/auth/sso"
)

var _ sso.TokenSpender = (*SSOBindings)(nil)

func TestSSOBindings_SpendOnce(t *testing.T) {
	b := openSSOBindings(t)
	ctx := context.Background()
	fresh, err := b.Spend(ctx, "jti:idp:1", time.Now().Add(time.Hour))
	require.NoError(t, err)
	assert.True(t, fresh)
	fresh, err = b.Spend(ctx, "jti:idp:1", time.Now().Add(time.Hour))
	require.NoError(t, err)
	assert.False(t, fresh, "a token is spent once")
}

func TestSSOBindings_SpendPurgesExpired(t *testing.T) {
	b := openSSOBindings(t)
	ctx := context.Background()
	_, err := b.Spend(ctx, "old", time.Now().Add(-time.Minute))
	require.NoError(t, err)
	_, err = b.Spend(ctx, "new", time.Now().Add(time.Hour))
	require.NoError(t, err)
	var n int
	require.NoError(t, b.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sso_spent_tokens`).Scan(&n))
	assert.Equal(t, 1, n, "expired keys are dropped")
}
