package approval_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
	"github.com/sebastienrousseau/rousseau-agent/internal/agent/approval"
	"github.com/sebastienrousseau/rousseau-agent/internal/auth/sso"
)

// captureEmitter records every audit callback so tests can
// assert on the exact records the manager emitted.
type captureEmitter struct {
	mu       sync.Mutex
	requests []approval.PendingRecord
	votes    []voteEvent
	resolved []resolvedEvent
}

type voteEvent struct {
	Token, Approver string
	Verdict         approval.Verdict
	Counted         bool
}

type resolvedEvent struct {
	Rec     approval.PendingRecord
	Verdict approval.Verdict
}

func (c *captureEmitter) EmitApprovalRequest(_ context.Context, r approval.PendingRecord) {
	c.mu.Lock()
	c.requests = append(c.requests, r)
	c.mu.Unlock()
}
func (c *captureEmitter) EmitApprovalVote(_ context.Context, tok, ap string, v approval.Verdict, counted bool) {
	c.mu.Lock()
	c.votes = append(c.votes, voteEvent{Token: tok, Approver: ap, Verdict: v, Counted: counted})
	c.mu.Unlock()
}
func (c *captureEmitter) EmitApprovalResolved(_ context.Context, r approval.PendingRecord, v approval.Verdict) {
	c.mu.Lock()
	c.resolved = append(c.resolved, resolvedEvent{Rec: r, Verdict: v})
	c.mu.Unlock()
}
func (c *captureEmitter) snapshot() (r []approval.PendingRecord, v []voteEvent, res []resolvedEvent) {
	c.mu.Lock()
	defer c.mu.Unlock()
	r = append([]approval.PendingRecord{}, c.requests...)
	v = append([]voteEvent{}, c.votes...)
	res = append([]resolvedEvent{}, c.resolved...)
	return
}

// -- Approver construction --

func TestNewApprover_RequiresPendingManager(t *testing.T) {
	_, err := approval.NewApprover(nil, nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "PendingManager")
}

func TestNewApprover_RejectsEmptyToolName(t *testing.T) {
	pm := approval.NewPendingManager(nil)
	_, err := approval.NewApprover([]approval.Rule{{Tool: "", NeededApprovals: 2}}, nil, pm)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty tool")
}

func TestNewApprover_RejectsZeroApprovals(t *testing.T) {
	pm := approval.NewPendingManager(nil)
	_, err := approval.NewApprover([]approval.Rule{{Tool: "bash", NeededApprovals: 0}}, nil, pm)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "NeededApprovals")
}

// -- Approver semantics --

func TestApprove_UncoveredToolDefersToInner(t *testing.T) {
	// Tool not in rule set → the wrapper is transparent.
	pm := approval.NewPendingManager(nil)
	app, err := approval.NewApprover(
		[]approval.Rule{{Tool: "bash", NeededApprovals: 2}},
		agent.AllowAllApprover{}, pm,
	)
	require.NoError(t, err)
	decision, _ := app.Approve(context.Background(), agent.ApprovalRequest{ToolName: "read"})
	assert.Equal(t, agent.DecisionAllow, decision)
}

func TestApprove_AnonymousRequesterDeniedImmediately(t *testing.T) {
	// Load-bearing: multi-party is meaningless without an
	// authenticated requester. Anonymous callers can't be
	// held to account so they must not even enqueue.
	pm := approval.NewPendingManager(nil)
	app, err := approval.NewApprover(
		[]approval.Rule{{Tool: "bash", NeededApprovals: 2}},
		agent.AllowAllApprover{}, pm,
	)
	require.NoError(t, err)
	decision, reason := app.Approve(context.Background(), agent.ApprovalRequest{ToolName: "bash"})
	assert.Equal(t, agent.DecisionDeny, decision)
	assert.Contains(t, reason, "authenticated requester")
}

func TestApprove_ReachesThresholdViaTwoApprovers(t *testing.T) {
	// Happy path: two distinct approvers /approve → the
	// waiting Approver unblocks with allow.
	emitter := &captureEmitter{}
	pm := approval.NewPendingManager(emitter)
	app, err := approval.NewApprover(
		[]approval.Rule{{Tool: "terraform", NeededApprovals: 2, Timeout: 2 * time.Second}},
		agent.AllowAllApprover{}, pm,
	)
	require.NoError(t, err)

	ctx := sso.WithIdentity(context.Background(), sso.Identity{Subject: "okta|alice"})
	req := agent.ApprovalRequest{ToolName: "terraform"}

	decisionCh := make(chan agent.Decision, 1)
	go func() {
		d, _ := app.Approve(ctx, req)
		decisionCh <- d
	}()

	// Wait for the request to enqueue, then vote.
	token := waitForRequest(t, emitter)
	res := pm.Vote(context.Background(), token, "okta|bob", approval.VerdictApprove)
	assert.Equal(t, approval.VoteResultCounted, res.Kind)
	res = pm.Vote(context.Background(), token, "okta|carol", approval.VerdictApprove)
	assert.Equal(t, approval.VoteResultResolvedApprove, res.Kind)

	select {
	case d := <-decisionCh:
		assert.Equal(t, agent.DecisionAllow, d)
	case <-time.After(1 * time.Second):
		t.Fatal("Approve did not resolve after threshold reached")
	}
}

func TestApprove_SingleDenyShortCircuits(t *testing.T) {
	// Property: one /deny wins over any number of pending
	// approves. Fastest-negative disposition.
	emitter := &captureEmitter{}
	pm := approval.NewPendingManager(emitter)
	app, err := approval.NewApprover(
		[]approval.Rule{{Tool: "delete", NeededApprovals: 3, Timeout: 2 * time.Second}},
		agent.AllowAllApprover{}, pm,
	)
	require.NoError(t, err)

	ctx := sso.WithIdentity(context.Background(), sso.Identity{Subject: "okta|alice"})
	req := agent.ApprovalRequest{ToolName: "delete"}

	decisionCh := make(chan agent.Decision, 1)
	go func() {
		d, _ := app.Approve(ctx, req)
		decisionCh <- d
	}()

	token := waitForRequest(t, emitter)
	// Two approves + one deny → deny wins.
	_ = pm.Vote(context.Background(), token, "okta|bob", approval.VerdictApprove)
	_ = pm.Vote(context.Background(), token, "okta|carol", approval.VerdictApprove)
	res := pm.Vote(context.Background(), token, "okta|dave", approval.VerdictDeny)
	assert.Equal(t, approval.VoteResultDenied, res.Kind)

	select {
	case d := <-decisionCh:
		assert.Equal(t, agent.DecisionDeny, d)
	case <-time.After(1 * time.Second):
		t.Fatal("deny did not short-circuit Approve")
	}
}

func TestApprove_SelfApproveRejectedAndDoesNotCount(t *testing.T) {
	// Load-bearing: the requester cannot approve themselves.
	// Otherwise a compromised user could bypass N-approval by
	// running the tool AND casting their own vote.
	emitter := &captureEmitter{}
	pm := approval.NewPendingManager(emitter)
	app, err := approval.NewApprover(
		[]approval.Rule{{Tool: "bash", NeededApprovals: 1, Timeout: 500 * time.Millisecond}},
		agent.AllowAllApprover{}, pm,
	)
	require.NoError(t, err)

	ctx := sso.WithIdentity(context.Background(), sso.Identity{Subject: "okta|alice"})
	req := agent.ApprovalRequest{ToolName: "bash"}

	decisionCh := make(chan agent.Decision, 1)
	go func() {
		d, _ := app.Approve(ctx, req)
		decisionCh <- d
	}()

	token := waitForRequest(t, emitter)
	// Alice tries to self-approve → rejected, NOT counted.
	res := pm.Vote(context.Background(), token, "okta|alice", approval.VerdictApprove)
	assert.Equal(t, approval.VoteResultSelfApproveRejected, res.Kind)

	// Even though NeededApprovals=1, no counting happened.
	// The request will time out (Timeout=500ms).
	select {
	case d := <-decisionCh:
		assert.Equal(t, agent.DecisionDeny, d, "self-approve must not satisfy threshold; request times out to deny")
	case <-time.After(1 * time.Second):
		t.Fatal("request should have timed out after 500ms")
	}
}

func TestApprove_TimeoutFiresDeny(t *testing.T) {
	emitter := &captureEmitter{}
	pm := approval.NewPendingManager(emitter)
	app, err := approval.NewApprover(
		[]approval.Rule{{Tool: "bash", NeededApprovals: 2, Timeout: 200 * time.Millisecond}},
		agent.AllowAllApprover{}, pm,
	)
	require.NoError(t, err)

	ctx := sso.WithIdentity(context.Background(), sso.Identity{Subject: "okta|alice"})
	req := agent.ApprovalRequest{ToolName: "bash"}
	start := time.Now()
	decision, reason := app.Approve(ctx, req)
	elapsed := time.Since(start)
	assert.Equal(t, agent.DecisionDeny, decision)
	assert.Contains(t, reason, "timed out")
	assert.GreaterOrEqual(t, elapsed, 200*time.Millisecond)
	assert.Less(t, elapsed, 1*time.Second)
}

func TestApprove_CtxCancelFiresDeny(t *testing.T) {
	// Agent aborted the Turn → the pending approval must
	// resolve to deny (never allow-via-cancellation).
	emitter := &captureEmitter{}
	pm := approval.NewPendingManager(emitter)
	app, err := approval.NewApprover(
		[]approval.Rule{{Tool: "bash", NeededApprovals: 2, Timeout: 5 * time.Second}},
		agent.AllowAllApprover{}, pm,
	)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	ctx = sso.WithIdentity(ctx, sso.Identity{Subject: "okta|alice"})

	decisionCh := make(chan agent.Decision, 1)
	go func() {
		d, _ := app.Approve(ctx, agent.ApprovalRequest{ToolName: "bash"})
		decisionCh <- d
	}()
	waitForRequest(t, emitter)
	cancel()
	select {
	case d := <-decisionCh:
		assert.Equal(t, agent.DecisionDeny, d)
	case <-time.After(1 * time.Second):
		t.Fatal("ctx-cancel did not resolve Approve")
	}
}

// -- PendingManager.Vote outcomes --

func TestVote_UnknownTokenReturnsUnknown(t *testing.T) {
	pm := approval.NewPendingManager(nil)
	res := pm.Vote(context.Background(), "never-existed", "okta|bob", approval.VerdictApprove)
	assert.Equal(t, approval.VoteResultUnknownToken, res.Kind)
}

func TestVote_AnonymousApproverRejected(t *testing.T) {
	// Router should refuse anonymous voters before calling
	// Vote, but the manager defends in depth.
	pm := approval.NewPendingManager(nil)
	res := pm.Vote(context.Background(), "any-token", "", approval.VerdictApprove)
	assert.Equal(t, approval.VoteResultAnonymousRejected, res.Kind)
}

func TestVote_DuplicateVoteIdempotent(t *testing.T) {
	emitter := &captureEmitter{}
	pm := approval.NewPendingManager(emitter)
	app, err := approval.NewApprover(
		[]approval.Rule{{Tool: "bash", NeededApprovals: 2, Timeout: 2 * time.Second}},
		agent.AllowAllApprover{}, pm,
	)
	require.NoError(t, err)

	ctx := sso.WithIdentity(context.Background(), sso.Identity{Subject: "okta|alice"})
	go func() { _, _ = app.Approve(ctx, agent.ApprovalRequest{ToolName: "bash"}) }()
	token := waitForRequest(t, emitter)

	res1 := pm.Vote(context.Background(), token, "okta|bob", approval.VerdictApprove)
	assert.Equal(t, approval.VoteResultCounted, res1.Kind)
	assert.Equal(t, 1, res1.Approvals)

	res2 := pm.Vote(context.Background(), token, "okta|bob", approval.VerdictApprove)
	assert.Equal(t, approval.VoteResultDuplicateVote, res2.Kind, "same voter must not double-count")
}

func TestVote_AfterResolveReturnsUnknown(t *testing.T) {
	// A vote that races the timeout / resolution must not
	// re-open a resolved record. Post-resolve the token is
	// evicted → unknown-token result. The alternative (leaving
	// records in memory forever) would leak.
	emitter := &captureEmitter{}
	pm := approval.NewPendingManager(emitter)
	app, err := approval.NewApprover(
		[]approval.Rule{{Tool: "bash", NeededApprovals: 1, Timeout: 100 * time.Millisecond}},
		agent.AllowAllApprover{}, pm,
	)
	require.NoError(t, err)
	ctx := sso.WithIdentity(context.Background(), sso.Identity{Subject: "okta|alice"})
	go func() { _, _ = app.Approve(ctx, agent.ApprovalRequest{ToolName: "bash"}) }()

	token := waitForRequest(t, emitter)
	time.Sleep(300 * time.Millisecond) // let it time out
	res := pm.Vote(context.Background(), token, "okta|bob", approval.VerdictApprove)
	assert.Equal(t, approval.VoteResultUnknownToken, res.Kind,
		"resolved-and-evicted tokens must not accept new votes")
}

// -- Audit emission --

func TestAudit_RequestAndResolveEmitted(t *testing.T) {
	emitter := &captureEmitter{}
	pm := approval.NewPendingManager(emitter)
	app, err := approval.NewApprover(
		[]approval.Rule{{Tool: "bash", NeededApprovals: 1, Timeout: 500 * time.Millisecond}},
		agent.AllowAllApprover{}, pm,
	)
	require.NoError(t, err)

	ctx := sso.WithIdentity(context.Background(), sso.Identity{Subject: "okta|alice"})
	decisionCh := make(chan agent.Decision, 1)
	go func() {
		d, _ := app.Approve(ctx, agent.ApprovalRequest{ToolName: "bash"})
		decisionCh <- d
	}()
	token := waitForRequest(t, emitter)
	_ = pm.Vote(context.Background(), token, "okta|bob", approval.VerdictApprove)
	<-decisionCh

	reqs, votes, resolved := emitter.snapshot()
	assert.Len(t, reqs, 1)
	assert.Equal(t, "bash", reqs[0].Tool)
	assert.Equal(t, "okta|alice", reqs[0].Requester)

	assert.Len(t, votes, 1)
	assert.True(t, votes[0].Counted)
	assert.Equal(t, "okta|bob", votes[0].Approver)

	require.Len(t, resolved, 1)
	assert.Equal(t, approval.VerdictApprove, resolved[0].Verdict)
}

func TestAudit_SelfApproveEmittedAsUncounted(t *testing.T) {
	// Load-bearing: a self-approve attempt is a security-
	// interesting event even though it doesn't count. The
	// audit trail must record it.
	emitter := &captureEmitter{}
	pm := approval.NewPendingManager(emitter)
	app, err := approval.NewApprover(
		[]approval.Rule{{Tool: "bash", NeededApprovals: 2, Timeout: 500 * time.Millisecond}},
		agent.AllowAllApprover{}, pm,
	)
	require.NoError(t, err)
	ctx := sso.WithIdentity(context.Background(), sso.Identity{Subject: "okta|alice"})
	go func() { _, _ = app.Approve(ctx, agent.ApprovalRequest{ToolName: "bash"}) }()
	token := waitForRequest(t, emitter)
	_ = pm.Vote(context.Background(), token, "okta|alice", approval.VerdictApprove)

	_, votes, _ := emitter.snapshot()
	require.Len(t, votes, 1)
	assert.Equal(t, "okta|alice", votes[0].Approver)
	assert.False(t, votes[0].Counted, "self-approve must emit as counted=false")
}

// -- VoteResult.String --

func TestVoteResult_StringCoversEveryKind(t *testing.T) {
	// Compact chat replies — must produce a legible string
	// for each kind. Property: no default "unknown result"
	// leaks for any real Kind.
	for _, k := range []approval.VoteResultKind{
		approval.VoteResultUnknownToken,
		approval.VoteResultAnonymousRejected,
		approval.VoteResultSelfApproveRejected,
		approval.VoteResultDuplicateVote,
		approval.VoteResultCounted,
		approval.VoteResultResolvedApprove,
		approval.VoteResultDenied,
		approval.VoteResultAlreadyResolved,
	} {
		s := approval.VoteResult{Kind: k, Approvals: 1, Needed: 2}.String()
		assert.NotEqual(t, "unknown result", s)
	}
	// Sanity: an unknown numeric kind DOES fall through to
	// "unknown result".
	s := approval.VoteResult{Kind: approval.VoteResultKind(9999)}.String()
	assert.Equal(t, "unknown result", s)
}

// -- helpers --

func waitForRequest(t *testing.T, emitter *captureEmitter) string {
	t.Helper()
	// Simple busy-wait; every test enqueues within a few ms
	// of the goroutine start.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		reqs, _, _ := emitter.snapshot()
		if len(reqs) > 0 {
			return reqs[0].Token
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("no request enqueued within deadline")
	return ""
}

// unused-import shim
var _ = strings.HasPrefix
var _ = errors.New
