package transport

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
	"github.com/sebastienrousseau/rousseau-agent/internal/auth/sso"
)

// stubDirectory is a minimal [sso.Directory] for router-side tests.
// The identity returned by VerifyToken is controlled per-instance
// so tests can exercise the router's happy path + failure branches
// without spinning up an OIDC server.
type stubDirectory struct {
	verify func(ctx context.Context, token string) (sso.Identity, error)
}

func (s stubDirectory) VerifyToken(ctx context.Context, tok string) (sso.Identity, error) {
	if s.verify == nil {
		return sso.Identity{}, sso.ErrTokenInvalid
	}
	return s.verify(ctx, tok)
}
func (stubDirectory) ResolveTransportID(context.Context, string, string) (sso.Identity, error) {
	return sso.Identity{}, sso.ErrNotFound
}

// memBindings is an in-memory [sso.BindingStore] — no SQLite in
// the router tests. Expiry logic mirrors the real SQLite impl.
type memBindings struct {
	mu        sync.Mutex
	entries   map[string]memBinding
	lookupErr error
}

type memBinding struct {
	id  sso.Identity
	exp time.Time
}

func newMemBindings() *memBindings {
	return &memBindings{entries: map[string]memBinding{}}
}

func (m *memBindings) key(t, e string) string { return t + "|" + e }

func (m *memBindings) Bind(_ context.Context, transport, externalID string, id sso.Identity, exp time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries[m.key(transport, externalID)] = memBinding{id: id, exp: exp}
	return nil
}

func (m *memBindings) Lookup(_ context.Context, transport, externalID string) (sso.Identity, bool, error) {
	if m.lookupErr != nil {
		return sso.Identity{}, false, m.lookupErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[m.key(transport, externalID)]
	if !ok || e.exp.Before(time.Now()) {
		return sso.Identity{}, false, nil
	}
	return e.id, true, nil
}

func (m *memBindings) Unbind(_ context.Context, transport, externalID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.entries, m.key(transport, externalID))
	return nil
}

func (m *memBindings) Count(context.Context) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, e := range m.entries {
		if e.exp.After(time.Now()) {
			n++
		}
	}
	return n, nil
}

// -- tests ---------------------------------------------------------

func TestRouter_LoginRunsBeforeAllowlist(t *testing.T) {
	// /login MUST fire even when the sender is not on the static
	// allowlist — that's the whole point (fresh users bootstrap
	// themselves in).
	store := newMemStore()
	jid := newMemJID()
	runner := &stubRunner{}
	bindings := newMemBindings()

	dir := stubDirectory{
		verify: func(_ context.Context, tok string) (sso.Identity, error) {
			return sso.Identity{Subject: "okta|alice", DisplayName: "Alice", ExpiresAt: time.Now().Add(time.Hour)}, nil
		},
	}
	router := NewRouter(runner, store, jid, silentLogger(), RouterOptions{
		Allowlist: []string{"+15550000000"}, // sender below not on it
		Transport: "whatsapp",
		SSO:       dir,
		SSOStore:  bindings,
	})

	reply, err := router.Handle(context.Background(), IncomingMessage{
		From: "+14155551212",
		Body: "/login valid-token",
	})
	require.NoError(t, err)
	assert.Contains(t, reply, "signed in as Alice")

	// The binding must be persisted for subsequent messages.
	id, ok, err := bindings.Lookup(context.Background(), "whatsapp", "+14155551212")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "okta|alice", id.Subject)
}

func TestRouter_LoginRejectsBadTokenWithGenericReply(t *testing.T) {
	// Fail-closed reveal: /login MUST NOT echo the underlying
	// verify error. A fuzz-happy stranger cannot tell "expired"
	// from "bad signature" from the reply.
	router := NewRouter(&stubRunner{}, newMemStore(), newMemJID(), silentLogger(), RouterOptions{
		Transport: "whatsapp",
		SSO: stubDirectory{
			verify: func(context.Context, string) (sso.Identity, error) {
				return sso.Identity{}, sso.ErrTokenExpired
			},
		},
		SSOStore: newMemBindings(),
	})
	reply, err := router.Handle(context.Background(), IncomingMessage{From: "+attacker", Body: "/login stale-token"})
	require.NoError(t, err)
	assert.Equal(t, "login: rejected", reply)
	assert.NotContains(t, reply, "expired")
	assert.NotContains(t, reply, "signature")
}

func TestRouter_LoginWrongArgCountShowsUsage(t *testing.T) {
	router := NewRouter(&stubRunner{}, newMemStore(), newMemJID(), silentLogger(), RouterOptions{
		Transport: "whatsapp",
		SSO:       stubDirectory{},
		SSOStore:  newMemBindings(),
	})
	reply, err := router.Handle(context.Background(), IncomingMessage{From: "+x", Body: "/login"})
	require.NoError(t, err)
	assert.Contains(t, reply, "usage:")
}

func TestRouter_SSOVerifiedSenderBypassesAllowlist(t *testing.T) {
	// After /login the sender's number MUST be treated as allowed
	// even though it never appeared in the static allowlist.
	bindings := newMemBindings()
	require.NoError(t, bindings.Bind(context.Background(), "whatsapp", "+14155559999",
		sso.Identity{Subject: "okta|bob"},
		time.Now().Add(time.Hour)))

	runner := &stubRunner{reply: agent.NewAssistantText("ok")}
	router := NewRouter(runner, newMemStore(), newMemJID(), silentLogger(), RouterOptions{
		Allowlist: []string{"+15550000000"}, // does NOT include +14155559999
		Transport: "whatsapp",
		SSOStore:  bindings,
		SSO:       stubDirectory{}, // dir non-nil so /login is enabled; irrelevant here
	})
	reply, err := router.Handle(context.Background(), IncomingMessage{
		From: "+14155559999",
		Body: "hello",
	})
	require.NoError(t, err)
	assert.Equal(t, "ok", reply, "SSO-bound sender must be allowed even without appearing in the static allowlist")
}

func TestRouter_SSOStoreErrorFailsClosed(t *testing.T) {
	// Fail-CLOSED: a store-side error must NOT open the gate.
	// This is the security-load-bearing test — an OIDC backend
	// hiccup that returned "ok" would leak the allowlist.
	bindings := newMemBindings()
	bindings.lookupErr = errors.New("db connection lost")
	router := NewRouter(&stubRunner{}, newMemStore(), newMemJID(), silentLogger(), RouterOptions{
		Allowlist: []string{"+15550000000"},
		Transport: "whatsapp",
		SSOStore:  bindings,
		SSO:       stubDirectory{},
	})
	reply, err := router.Handle(context.Background(), IncomingMessage{From: "+not-allowlisted", Body: "hello"})
	require.NoError(t, err)
	assert.Empty(t, reply, "store failure must deny access, not silently allow")
}

func TestRouter_LogoutRemovesBinding(t *testing.T) {
	bindings := newMemBindings()
	require.NoError(t, bindings.Bind(context.Background(), "whatsapp", "+bye",
		sso.Identity{Subject: "okta|out"},
		time.Now().Add(time.Hour)))

	router := NewRouter(&stubRunner{}, newMemStore(), newMemJID(), silentLogger(), RouterOptions{
		Transport: "whatsapp",
		SSO:       stubDirectory{},
		SSOStore:  bindings,
	})
	reply, err := router.Handle(context.Background(), IncomingMessage{From: "+bye", Body: "/logout"})
	require.NoError(t, err)
	assert.Equal(t, "signed out", reply)

	_, ok, err := bindings.Lookup(context.Background(), "whatsapp", "+bye")
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestRouter_NoSSOConfiguredSkipsCommands(t *testing.T) {
	// Without an SSO directory the /login command must fall
	// through — the message reaches the LLM (or the allowlist
	// blocks it). Never surface an "SSO not configured" reply.
	runner := &stubRunner{reply: agent.NewAssistantText("llm-reply")}
	router := NewRouter(runner, newMemStore(), newMemJID(), silentLogger(), RouterOptions{
		Transport: "whatsapp",
		// SSO left nil
	})
	reply, err := router.Handle(context.Background(), IncomingMessage{From: "+x", Body: "/login whatever"})
	require.NoError(t, err)
	assert.Equal(t, "llm-reply", reply, "no-SSO daemon must treat /login as a normal message")
}

func TestRouter_LoginBindingBoundedByConfiguredTTL(t *testing.T) {
	// The configured TTL must clip a token that would otherwise
	// bind for its full (long-lived) exp. Load-bearing so a
	// mis-issued 1-year token doesn't unlock a chat handle for
	// a year.
	bindings := newMemBindings()
	router := NewRouter(&stubRunner{}, newMemStore(), newMemJID(), silentLogger(), RouterOptions{
		Transport:     "whatsapp",
		SSOStore:      bindings,
		SSOBindingTTL: 15 * time.Minute,
		SSO: stubDirectory{
			verify: func(context.Context, string) (sso.Identity, error) {
				return sso.Identity{
					Subject:   "okta|longlived",
					ExpiresAt: time.Now().Add(365 * 24 * time.Hour),
				}, nil
			},
		},
	})
	_, err := router.Handle(context.Background(), IncomingMessage{From: "+ttl", Body: "/login t"})
	require.NoError(t, err)

	// Poke into memBindings — the entry's exp must land near
	// now+15m, NOT now+1y.
	bindings.mu.Lock()
	entry := bindings.entries["whatsapp|+ttl"]
	bindings.mu.Unlock()
	assert.WithinDuration(t, time.Now().Add(15*time.Minute), entry.exp, 5*time.Second)
}
