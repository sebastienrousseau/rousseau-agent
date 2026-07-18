package oauth

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBroker_ResolveDefaults covers the default fallbacks for
// resolveAddr and resolveTimeout.
func TestBroker_ResolveDefaults(t *testing.T) {
	v, _ := newVault(t)
	b := NewBroker(v)
	assert.Equal(t, "127.0.0.1:8765", b.resolveAddr())
	assert.Equal(t, 5*time.Minute, b.resolveTimeout())

	b.CallbackAddr = "127.0.0.1:9999"
	b.CallbackTimeout = time.Second
	assert.Equal(t, "127.0.0.1:9999", b.resolveAddr())
	assert.Equal(t, time.Second, b.resolveTimeout())
}

// TestRandomState_ProducesUniqueHex confirms the state minter's
// output is 32 hex chars and unique across calls.
func TestRandomState_ProducesUniqueHex(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 20; i++ {
		s, err := randomState()
		require.NoError(t, err)
		assert.Len(t, s, 32)
		assert.False(t, seen[s], "duplicate state %s", s)
		seen[s] = true
	}
}

// TestBroker_Start_AgesOutOldInflight covers the >30-minute
// eviction branch in Start.
func TestBroker_Start_AgesOutOldInflight(t *testing.T) {
	v, _ := newVault(t)
	b := NewBroker(v)
	b.Register(&fakeProvider{name: "google", authURL: "http://x"})
	// Seed an ancient in-flight state that Start will evict on next call.
	b.mu.Lock()
	b.inflight["stale"] = inflightState{provider: "google", created: time.Now().Add(-time.Hour)}
	b.mu.Unlock()

	_, _, err := b.Start("google")
	require.NoError(t, err)

	b.mu.Lock()
	_, still := b.inflight["stale"]
	b.mu.Unlock()
	assert.False(t, still, "stale state should be aged out")
}

// TestBroker_Complete_ProviderVanishedMidFlow covers the branch
// where the provider is deregistered between Start and Complete.
func TestBroker_Complete_ProviderVanishedMidFlow(t *testing.T) {
	v, _ := newVault(t)
	b := NewBroker(v)
	b.Register(&fakeProvider{name: "google", authURL: "http://x",
		exchange: func(context.Context, string) (*Token, error) { return &Token{AccessToken: "at"}, nil }})
	_, state, err := b.Start("google")
	require.NoError(t, err)

	// Remove the provider.
	b.providers = map[string]Provider{}
	_, err = b.Complete(context.Background(), state, "code", "acct")
	assert.ErrorContains(t, err, "vanished")
}

// TestBroker_Load_UnknownProviderErrors covers the branch where
// Load looks up a token but the provider is no longer registered.
func TestBroker_Load_UnknownProviderErrors(t *testing.T) {
	v, _ := newVault(t)
	b := NewBroker(v)
	// Seed a token directly into the vault so Load finds it, then
	// register no provider so the refresh-eligible branch fails on
	// provider lookup.
	require.NoError(t, v.Put(context.Background(), "orphan", "acct", &Token{
		AccessToken: "at-old", RefreshToken: "rt",
		Expiry: time.Now().Add(-time.Minute),
	}))
	_, err := b.Load(context.Background(), "orphan", "acct")
	assert.ErrorContains(t, err, "unknown provider")
}

// TestBroker_Load_RefreshError propagates the refresh-time error.
func TestBroker_Load_RefreshError(t *testing.T) {
	v, _ := newVault(t)
	b := NewBroker(v)
	b.Register(&fakeProvider{
		name: "google",
		refresh: func(context.Context, string) (*Token, error) {
			return nil, assert.AnError
		},
	})
	require.NoError(t, v.Put(context.Background(), "google", "acct", &Token{
		AccessToken: "at-old", RefreshToken: "rt",
		Expiry: time.Now().Add(-time.Minute),
	}))
	_, err := b.Load(context.Background(), "google", "acct")
	assert.Error(t, err)
}
