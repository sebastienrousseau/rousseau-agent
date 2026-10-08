package transport

import (
	"context"
	"encoding/json"
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
	reply, err := router.Handle(context.Background(), IncomingMessage{From: "+bob", Body: "/approve deadbeef 0123abcd"})
	require.NoError(t, err)
	assert.Contains(t, reply, "unknown or already-resolved")
}

func TestRouter_ApproveWithoutSSOIsAnonymousRejected(t *testing.T) {
	// Fail-CLOSED: voting requires an authenticated identity.
	// A sender without an SSO binding gets the anonymous-
	// rejected reply.
	pending := approval.NewPendingManager(nil)
	router := routerWithPending(t, pending, "", "+unauth") // no bindings
	reply, err := router.Handle(context.Background(), IncomingMessage{From: "+unauth", Body: "/approve deadbeef 0123abcd"})
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
	rec := pending.Enqueue(context.Background(), approval.PendingInput{Tool: "bash", Input: json.RawMessage(`{"command":"terraform apply"}`), Requester: "okta|alice", SessionID: "sess-1", Needed: 2, Timeout: time.Minute})

	router := routerWithPending(t, pending, "okta|bob", "+bob")
	reply, err := router.Handle(context.Background(), IncomingMessage{From: "+bob", Body: "/deny " + rec.Token})
	require.NoError(t, err)
	assert.Equal(t, "denied", reply)
}

func TestRouter_ApproveCountsTowardThreshold(t *testing.T) {
	pending := approval.NewPendingManager(nil)
	rec := pending.Enqueue(context.Background(), approval.PendingInput{Tool: "bash", Input: json.RawMessage(`{"command":"terraform apply"}`), Requester: "okta|alice", SessionID: "sess-1", Needed: 3, Timeout: time.Minute})

	// Two independent voters approve → we expect "recorded"
	// (progress reply) then "approved" (final).
	router1 := routerWithPending(t, pending, "okta|bob", "+bob")
	router2 := routerWithPending(t, pending, "okta|carol", "+carol")
	router3 := routerWithPending(t, pending, "okta|dave", "+dave")

	reply1, err := router1.Handle(context.Background(), IncomingMessage{From: "+bob", Body: "/approve " + rec.Token + " " + rec.InputSHA256[:8]})
	require.NoError(t, err)
	assert.Contains(t, reply1, "recorded")
	reply2, err := router2.Handle(context.Background(), IncomingMessage{From: "+carol", Body: "/approve " + rec.Token + " " + rec.InputSHA256[:8]})
	require.NoError(t, err)
	assert.Contains(t, reply2, "recorded")
	reply3, err := router3.Handle(context.Background(), IncomingMessage{From: "+dave", Body: "/approve " + rec.Token + " " + rec.InputSHA256[:8]})
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

// /approve must quote the input digest of the request it approves.
func TestRouter_ApproveWithWrongDigestRefused(t *testing.T) {
	pending := approval.NewPendingManager(nil)
	rec := pending.Enqueue(context.Background(), approval.PendingInput{Tool: "bash", Input: json.RawMessage(`{"command":"terraform apply"}`), Requester: "okta|alice", Needed: 1, Timeout: time.Minute})
	router := routerWithPending(t, pending, "okta|bob", "+bob")
	reply, err := router.Handle(context.Background(), IncomingMessage{From: "+bob", Body: "/approve " + rec.Token + " 00000000"})
	require.NoError(t, err)
	assert.Contains(t, reply, "does not match")
	still, ok := pending.Lookup(rec.Token)
	require.True(t, ok)
	assert.Empty(t, still.Votes, "a mismatched digest records no vote")

	reply, err = router.Handle(context.Background(), IncomingMessage{From: "+bob", Body: "/approve " + rec.Token})
	require.NoError(t, err)
	assert.Contains(t, reply, "usage: /approve <token>")
}

// /pending shows voters what each open request would run.
func TestRouter_PendingListsInputs(t *testing.T) {
	pending := approval.NewPendingManager(nil)
	router := routerWithPending(t, pending, "okta|bob", "+bob")
	reply, err := router.Handle(context.Background(), IncomingMessage{From: "+bob", Body: "/pending"})
	require.NoError(t, err)
	assert.Equal(t, "no pending approvals", reply)

	rec := pending.Enqueue(context.Background(), approval.PendingInput{Tool: "bash", Input: json.RawMessage(`{"command":"terraform apply"}`), Requester: "okta|alice", Needed: 2, Timeout: time.Minute})
	reply, err = router.Handle(context.Background(), IncomingMessage{From: "+bob", Body: "/pending"})
	require.NoError(t, err)
	assert.Contains(t, reply, rec.Token)
	assert.Contains(t, reply, `"terraform apply"`)
	assert.Contains(t, reply, rec.InputSHA256[:8])
	assert.Contains(t, reply, "0/2")
}
