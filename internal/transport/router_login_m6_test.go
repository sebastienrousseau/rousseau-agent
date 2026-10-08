package transport

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/auth/sso"
)

func loginRouter(t *testing.T, id sso.Identity) (*Router, *memBindings) {
	t.Helper()
	bindings := newMemBindings()
	r := NewRouter(&stubRunner{}, newMemStore(), newMemJID(), silentLogger(), RouterOptions{
		Transport: "slack",
		Allowlist: []string{"nobody"},
		SSO: stubDirectory{verify: func(context.Context, string) (sso.Identity, error) {
			return id, nil
		}},
		SSOStore: bindings,
	})
	return r, bindings
}

func bound(t *testing.T, b *memBindings, from string) bool {
	t.Helper()
	_, ok, err := b.Lookup(context.Background(), "slack", from)
	require.NoError(t, err)
	return ok
}

// A token whose transport claim names another handle must not bind
// the sender who pasted it.
func TestLogin_TokenForOtherHandleRejected(t *testing.T) {
	r, b := loginRouter(t, sso.Identity{Subject: "okta|alice", TransportIDs: map[string]string{"slack": "UALICE"}})
	reply, err := r.Handle(context.Background(), IncomingMessage{From: "UMALLORY", IsDirect: true, Body: "/login tok"})
	require.NoError(t, err)
	assert.Equal(t, "login: rejected", reply)
	assert.False(t, bound(t, b, "UMALLORY"))

	reply, err = r.Handle(context.Background(), IncomingMessage{From: "UALICE", IsDirect: true, Body: "/login tok"})
	require.NoError(t, err)
	assert.Contains(t, reply, "signed in")
	assert.True(t, bound(t, b, "UALICE"))
}

// A token is good for one /login only.
func TestLogin_ReplayRejected(t *testing.T) {
	r, b := loginRouter(t, sso.Identity{Subject: "okta|alice", TokenID: "jti:idp:1"})
	reply, err := r.Handle(context.Background(), IncomingMessage{From: "UALICE", IsDirect: true, Body: "/login tok"})
	require.NoError(t, err)
	assert.Contains(t, reply, "signed in")

	reply, err = r.Handle(context.Background(), IncomingMessage{From: "UOTHER", IsDirect: true, Body: "/login tok"})
	require.NoError(t, err)
	assert.Equal(t, "login: rejected", reply, "a token seen once is spent")
	assert.False(t, bound(t, b, "UOTHER"))
}

// Without a TokenID the router keys on the token text itself.
func TestLogin_ReplayRejectedWithoutTokenID(t *testing.T) {
	r, _ := loginRouter(t, sso.Identity{Subject: "okta|alice"})
	_, err := r.Handle(context.Background(), IncomingMessage{From: "UALICE", IsDirect: true, Body: "/login tok"})
	require.NoError(t, err)
	reply, err := r.Handle(context.Background(), IncomingMessage{From: "UOTHER", IsDirect: true, Body: "/login tok"})
	require.NoError(t, err)
	assert.Equal(t, "login: rejected", reply)
}

// A token pasted in a group is visible to every member: bind nothing.
func TestLogin_InGroupRefused(t *testing.T) {
	r, b := loginRouter(t, sso.Identity{Subject: "okta|alice"})
	reply, err := r.Handle(context.Background(), IncomingMessage{From: "UALICE", Conversation: "CGROUP", Body: "/login tok"})
	require.NoError(t, err)
	assert.Equal(t, "send /login in a direct message", reply)
	assert.False(t, bound(t, b, "UALICE"))
}
