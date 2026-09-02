package transport

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent/approval"
	"github.com/sebastienrousseau/rousseau-agent/internal/auth/sso"
)

// helper: mint a Router with a real PendingManager + a
// pre-authenticated voter (SSO binding for the caller).
func routerWithPending(t *testing.T, pending *approval.PendingManager, voterIdentity string, voterFrom string) *Router {
	t.Helper()
	bindings := newMemBindings()
	if voterIdentity != "" {
		require.NoError(t, bindings.Bind(context.Background(), "whatsapp", voterFrom,
			sso.Identity{Subject: voterIdentity}, time.Now().Add(time.Hour)))
	}
	return NewRouter(&stubRunner{}, newMemStore(), newMemJID(), silentLogger(), RouterOptions{
		Transport: "whatsapp",
		SSO:       stubDirectory{},
		SSOStore:  bindings,
		Approvals: pending,
	})
}

func TestRouter_ApproveUnknownTokenReplyIsUsable(t *testing.T) {
	// A vote on a stale token must produce a legible reply
	// (not a stack trace, not an empty string). This is the
	// UX contract for the chat command.
	pending := approval.NewPendingManager(nil)
	router := routerWithPending(t, pending, "okta|bob", "+bob")
	reply, err := router.Handle(context.Background(), IncomingMessage{From: "+bob", Body: "/approve deadbeef"})
	require.NoError(t, err)
	assert.Contains(t, reply, "unknown or already-resolved")
}

func TestRouter_ApproveWithoutSSOIsAnonymousRejected(t *testing.T) {
	// Fail-CLOSED: voting requires an authenticated identity.
	// A sender without an SSO binding gets the anonymous-
	// rejected reply.
	pending := approval.NewPendingManager(nil)
	router := routerWithPending(t, pending, "", "+unauth") // no bindings
	reply, err := router.Handle(context.Background(), IncomingMessage{From: "+unauth", Body: "/approve deadbeef"})
	require.NoError(t, err)
	assert.Contains(t, reply, "sign in via /login")
}

func TestRouter_ApproveWithoutArgShowsUsage(t *testing.T) {
	pending := approval.NewPendingManager(nil)
	router := routerWithPending(t, pending, "okta|bob", "+bob")
	reply, err := router.Handle(context.Background(), IncomingMessage{From: "+bob", Body: "/approve"})
	require.NoError(t, err)
	assert.Contains(t, reply, "usage:")
}

func TestRouter_DenyShortCircuitsPendingRecord(t *testing.T) {
	// End-to-end: enqueue a record via the PendingManager
	// directly (simulates the approver having enqueued it),
	// then have the router's /deny handler shortcut it.
	pending := approval.NewPendingManager(nil)
	rec := pending.Enqueue(context.Background(), "bash", "okta|alice", "sess-1", 2, time.Minute)

	router := routerWithPending(t, pending, "okta|bob", "+bob")
	reply, err := router.Handle(context.Background(), IncomingMessage{From: "+bob", Body: "/deny " + rec.Token})
	require.NoError(t, err)
	assert.Equal(t, "denied", reply)
}

func TestRouter_ApproveCountsTowardThreshold(t *testing.T) {
	pending := approval.NewPendingManager(nil)
	rec := pending.Enqueue(context.Background(), "bash", "okta|alice", "sess-1", 3, time.Minute)

	// Two independent voters approve → we expect "recorded"
	// (progress reply) then "approved" (final).
	router1 := routerWithPending(t, pending, "okta|bob", "+bob")
	router2 := routerWithPending(t, pending, "okta|carol", "+carol")
	router3 := routerWithPending(t, pending, "okta|dave", "+dave")

	reply1, err := router1.Handle(context.Background(), IncomingMessage{From: "+bob", Body: "/approve " + rec.Token})
	require.NoError(t, err)
	assert.Contains(t, reply1, "recorded")
	reply2, err := router2.Handle(context.Background(), IncomingMessage{From: "+carol", Body: "/approve " + rec.Token})
	require.NoError(t, err)
	assert.Contains(t, reply2, "recorded")
	reply3, err := router3.Handle(context.Background(), IncomingMessage{From: "+dave", Body: "/approve " + rec.Token})
	require.NoError(t, err)
	assert.Contains(t, reply3, "approved")
}

func TestRouter_NoPendingManagerSkipsCommand(t *testing.T) {
	// Property: without a PendingManager wired, /approve
	// falls through to the LLM. Verifies opt-in: an OSS
	// install doesn't grow a new chat command surface.
	router := NewRouter(&stubRunner{}, newMemStore(), newMemJID(), silentLogger(), RouterOptions{
		Transport: "whatsapp",
		// Approvals omitted
	})
	// Without an SSO directory or approvals, /approve should
	// reach the runner (which is a stubRunner returning an
	// empty message). The important thing is no panic + no
	// "unknown or already-resolved" reply.
	require.NotPanics(t, func() {
		_, err := router.Handle(context.Background(), IncomingMessage{From: "+x", Body: "/approve foo"})
		require.NoError(t, err)
	})
}
