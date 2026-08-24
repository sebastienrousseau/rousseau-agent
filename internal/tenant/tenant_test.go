package tenant_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/tenant"
)

func TestNewMapResolver_RejectsMissingID(t *testing.T) {
	_, err := tenant.NewMapResolver([]tenant.Config{{}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ID")
}

func TestNewMapResolver_RejectsDuplicateIDs(t *testing.T) {
	_, err := tenant.NewMapResolver([]tenant.Config{
		{ID: "a"}, {ID: "a"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate")
}

func TestMapResolver_ExactMatchWins(t *testing.T) {
	r, err := tenant.NewMapResolver([]tenant.Config{
		{ID: "team-a", Allowlist: []string{"whatsapp:+1"}},
		{ID: "team-b", Allowlist: []string{"whatsapp:+2"}},
	})
	require.NoError(t, err)
	got, err := r.Resolve(context.Background(), "whatsapp", "+2")
	require.NoError(t, err)
	assert.Equal(t, tenant.ID("team-b"), got)
}

func TestMapResolver_TransportAgnosticMatch(t *testing.T) {
	r, err := tenant.NewMapResolver([]tenant.Config{
		{ID: "personal", Allowlist: []string{"+15551234567"}},
	})
	require.NoError(t, err)
	// Same sender on either transport resolves.
	for _, tp := range []string{"whatsapp", "sms", "telegram"} {
		got, err := r.Resolve(context.Background(), tp, "+15551234567")
		require.NoError(t, err)
		assert.Equal(t, tenant.ID("personal"), got, tp)
	}
}

func TestMapResolver_CatchAllTenant(t *testing.T) {
	r, err := tenant.NewMapResolver([]tenant.Config{
		{ID: "team-a", Allowlist: []string{"whatsapp:+1"}},
		{ID: "fallback", Allowlist: []string{"*"}},
	})
	require.NoError(t, err)
	// Exact match still wins over the catch-all.
	got, err := r.Resolve(context.Background(), "whatsapp", "+1")
	require.NoError(t, err)
	assert.Equal(t, tenant.ID("team-a"), got)
	// Anything else falls to the catch-all.
	got, err = r.Resolve(context.Background(), "sms", "+99")
	require.NoError(t, err)
	assert.Equal(t, tenant.ID("fallback"), got)
}

func TestMapResolver_TransportWildcardMatch(t *testing.T) {
	r, err := tenant.NewMapResolver([]tenant.Config{
		{ID: "power-users", Allowlist: []string{"*:+15555"}},
	})
	require.NoError(t, err)
	got, err := r.Resolve(context.Background(), "signal", "+15555")
	require.NoError(t, err)
	assert.Equal(t, tenant.ID("power-users"), got)
}

func TestMapResolver_NoMatchReturnsEmpty(t *testing.T) {
	r, err := tenant.NewMapResolver([]tenant.Config{
		{ID: "t1", Allowlist: []string{"whatsapp:+1"}},
	})
	require.NoError(t, err)
	got, err := r.Resolve(context.Background(), "sms", "+99")
	require.NoError(t, err)
	assert.Equal(t, tenant.ID(""), got)
}

func TestConfigFor_ReturnsRegisteredConfig(t *testing.T) {
	r, err := tenant.NewMapResolver([]tenant.Config{
		{ID: "team-a", Credentials: map[string]string{"anthropic": "sk-a"}},
	})
	require.NoError(t, err)
	got, ok := r.ConfigFor("team-a")
	require.True(t, ok)
	assert.Equal(t, "sk-a", got.Credentials["anthropic"])
	_, ok = r.ConfigFor("nope")
	assert.False(t, ok)
}

func TestAll_ReturnsCopy(t *testing.T) {
	orig := []tenant.Config{{ID: "a"}, {ID: "b"}}
	r, err := tenant.NewMapResolver(orig)
	require.NoError(t, err)
	got := r.All()
	require.Len(t, got, 2)
	got[0].ID = "mutated"
	fresh := r.All()
	assert.Equal(t, tenant.ID("a"), fresh[0].ID)
}

func TestWithID_FromContext_RoundTrip(t *testing.T) {
	ctx := tenant.WithID(context.Background(), "team-a")
	assert.Equal(t, tenant.ID("team-a"), tenant.FromContext(ctx))
}

func TestFromContext_EmptyWhenUnset(t *testing.T) {
	assert.Equal(t, tenant.ID(""), tenant.FromContext(context.Background()))
}

func TestWithID_EmptyIsNoop(t *testing.T) {
	base := tenant.WithID(context.Background(), "existing")
	after := tenant.WithID(base, "")
	assert.Equal(t, tenant.ID("existing"), tenant.FromContext(after))
}
