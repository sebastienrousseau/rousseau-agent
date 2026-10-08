package sso

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMemorySpender(t *testing.T) {
	now := time.Unix(1000, 0)
	m := &MemorySpender{now: func() time.Time { return now }}
	ctx := context.Background()
	fresh, err := m.Spend(ctx, "k", now.Add(time.Minute))
	require.NoError(t, err)
	assert.True(t, fresh)
	fresh, err = m.Spend(ctx, "k", now.Add(time.Minute))
	require.NoError(t, err)
	assert.False(t, fresh, "a key is spent once")

	now = now.Add(2 * time.Minute)
	fresh, err = m.Spend(ctx, "other", now.Add(time.Minute))
	require.NoError(t, err)
	assert.True(t, fresh)
	assert.NotContains(t, m.spent, "k", "expired keys are dropped")
}
