package transport

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/auth/sso"
	"github.com/sebastienrousseau/rousseau-agent/internal/observability/audit_egress"
)

// captureAuditSink is an in-memory Sink for router-audit tests.
// Records every Emit so tests can assert on the exact category /
// verb / actor / result the router stamped.
type captureAuditSink struct {
	mu      sync.Mutex
	records []audit_egress.Record
}

func (c *captureAuditSink) Emit(_ context.Context, r audit_egress.Record) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.records = append(c.records, r)
	return nil
}

func (c *captureAuditSink) Close(context.Context) error { return nil }

func (c *captureAuditSink) snapshot() []audit_egress.Record {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]audit_egress.Record, len(c.records))
	copy(out, c.records)
	return out
}

// -- /login audit emission --

func TestRouter_LoginSuccessEmitsAuditRecord(t *testing.T) {
	audit := &captureAuditSink{}
	router := NewRouter(&stubRunner{}, newMemStore(), newMemJID(), silentLogger(), RouterOptions{
		Transport: "whatsapp",
		SSO: stubDirectory{verify: func(context.Context, string) (sso.Identity, error) {
			return sso.Identity{
				Subject: "okta|alice", DisplayName: "Alice",
				ExpiresAt: time.Now().Add(time.Hour),
			}, nil
		}},
		SSOStore:  newMemBindings(),
		AuditSink: audit,
	})
	_, err := router.Handle(context.Background(), IncomingMessage{From: "+42", Body: "/login t"})
	require.NoError(t, err)

	recs := audit.snapshot()
	require.Len(t, recs, 1)
	assert.Equal(t, "auth", recs[0].Category)
	assert.Equal(t, "login", recs[0].Verb)
	assert.Equal(t, "success", recs[0].Result)
	assert.Equal(t, "okta|alice", recs[0].Actor)
	assert.Equal(t, "+42", recs[0].Object)
	assert.Equal(t, "whatsapp", recs[0].Detail["transport"])
	assert.NotEmpty(t, recs[0].Detail["expires_at"])
}

func TestRouter_LoginFailureEmitsDeniedAuditRecord(t *testing.T) {
	// Fail-closed reveal: the REPLY to the sender is
	// "login: rejected" (verified in another test). The AUDIT
	// record — visible only to the operator — must include the
	// underlying reason so SIEMs can alert on repeated denials.
	audit := &captureAuditSink{}
	router := NewRouter(&stubRunner{}, newMemStore(), newMemJID(), silentLogger(), RouterOptions{
		Transport: "whatsapp",
		SSO: stubDirectory{verify: func(context.Context, string) (sso.Identity, error) {
			return sso.Identity{}, sso.ErrTokenExpired
		}},
		SSOStore:  newMemBindings(),
		AuditSink: audit,
	})
	_, err := router.Handle(context.Background(), IncomingMessage{From: "+attacker", Body: "/login bad"})
	require.NoError(t, err)

	recs := audit.snapshot()
	require.Len(t, recs, 1)
	assert.Equal(t, "denied", recs[0].Result)
	assert.Equal(t, "+attacker", recs[0].Object,
		"failed login must still name the target in Object")
	assert.Empty(t, recs[0].Actor,
		"failed login has no verified identity yet — Actor stays empty")
	assert.Contains(t, recs[0].Detail["reason"], "expired",
		"operator-facing audit MUST carry the failure reason")
}

func TestRouter_LoginStoreErrorEmitsErrorAuditRecord(t *testing.T) {
	// Rare-but-real: token verified but the SQLite Bind
	// fails (disk full, etc). Audit MUST record it so the
	// operator sees "why is Alice's /login not persisting?"
	// without grepping logs.
	audit := &captureAuditSink{}
	bindings := &brokenBindings{err: errors.New("disk full")}
	router := NewRouter(&stubRunner{}, newMemStore(), newMemJID(), silentLogger(), RouterOptions{
		Transport: "whatsapp",
		SSO: stubDirectory{verify: func(context.Context, string) (sso.Identity, error) {
			return sso.Identity{Subject: "okta|alice", ExpiresAt: time.Now().Add(time.Hour)}, nil
		}},
		SSOStore:  bindings,
		AuditSink: audit,
	})
	_, err := router.Handle(context.Background(), IncomingMessage{From: "+42", Body: "/login t"})
	require.NoError(t, err)

	recs := audit.snapshot()
	require.Len(t, recs, 1)
	assert.Equal(t, "error", recs[0].Result)
	assert.Contains(t, recs[0].Detail["reason"], "disk full")
}

// -- /logout audit emission --

func TestRouter_LogoutSuccessEmitsAuditRecord(t *testing.T) {
	audit := &captureAuditSink{}
	bindings := newMemBindings()
	require.NoError(t, bindings.Bind(context.Background(), "whatsapp", "+42",
		sso.Identity{Subject: "okta|alice"}, time.Now().Add(time.Hour)))

	router := NewRouter(&stubRunner{}, newMemStore(), newMemJID(), silentLogger(), RouterOptions{
		Transport: "whatsapp",
		SSO:       stubDirectory{},
		SSOStore:  bindings,
		AuditSink: audit,
	})
	_, err := router.Handle(context.Background(), IncomingMessage{From: "+42", Body: "/logout"})
	require.NoError(t, err)

	recs := audit.snapshot()
	require.Len(t, recs, 1)
	assert.Equal(t, "logout", recs[0].Verb)
	assert.Equal(t, "success", recs[0].Result)
	assert.Equal(t, "okta|alice", recs[0].Actor,
		"logout must resolve actor BEFORE unbinding so audit names the identity")
}

func TestRouter_LogoutWithoutPriorBindingSucceedsWithEmptyActor(t *testing.T) {
	// Idempotent path: logout with no prior login still lands
	// an audit record so the operator can see attempts. Actor
	// is empty (no identity was ever bound).
	audit := &captureAuditSink{}
	router := NewRouter(&stubRunner{}, newMemStore(), newMemJID(), silentLogger(), RouterOptions{
		Transport: "whatsapp",
		SSO:       stubDirectory{},
		SSOStore:  newMemBindings(),
		AuditSink: audit,
	})
	_, err := router.Handle(context.Background(), IncomingMessage{From: "+never-logged-in", Body: "/logout"})
	require.NoError(t, err)

	recs := audit.snapshot()
	require.Len(t, recs, 1)
	assert.Equal(t, "success", recs[0].Result)
	assert.Empty(t, recs[0].Actor)
}

func TestRouter_NoAuditSinkIsNoop(t *testing.T) {
	// The nil-sink path must not panic and must not emit
	// anything — property that the sink is truly opt-in.
	router := NewRouter(&stubRunner{}, newMemStore(), newMemJID(), silentLogger(), RouterOptions{
		Transport: "whatsapp",
		SSO: stubDirectory{verify: func(context.Context, string) (sso.Identity, error) {
			return sso.Identity{Subject: "okta|alice", ExpiresAt: time.Now().Add(time.Hour)}, nil
		}},
		SSOStore: newMemBindings(),
		// AuditSink omitted — must be safe.
	})
	require.NotPanics(t, func() {
		_, err := router.Handle(context.Background(), IncomingMessage{From: "+42", Body: "/login t"})
		require.NoError(t, err)
	})
}

// -- brokenBindings for the "store error" test --

type brokenBindings struct {
	err error
}

func (b *brokenBindings) Bind(context.Context, string, string, sso.Identity, time.Time) error {
	return b.err
}
func (b *brokenBindings) Lookup(context.Context, string, string) (sso.Identity, bool, error) {
	return sso.Identity{}, false, nil
}
func (b *brokenBindings) Unbind(context.Context, string, string) error { return nil }
func (b *brokenBindings) Count(context.Context) (int, error)           { return 0, nil }
