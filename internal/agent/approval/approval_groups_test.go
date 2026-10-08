package approval_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
	"github.com/sebastienrousseau/rousseau-agent/internal/agent/approval"
	"github.com/sebastienrousseau/rousseau-agent/internal/auth/sso"
)

// L-24: a rule's approver_groups restrict who may vote. Without them
// any signed-in subject could approve (or deny) a governed call.

// voterCtx is a chat context for subject in groups.
func voterCtx(subject string, groups ...string) context.Context {
	return sso.WithIdentity(context.Background(), sso.Identity{Subject: subject, Groups: groups})
}

// startGoverned enqueues one bash request by alice under rule and
// returns the token plus the channel its decision lands on.
func startGoverned(t *testing.T, rule approval.Rule) (*approval.PendingManager, *captureEmitter, string, <-chan agent.Decision) {
	t.Helper()
	emitter := &captureEmitter{}
	pm := approval.NewPendingManager(emitter)
	app, err := approval.NewApprover([]approval.Rule{rule}, agent.AllowAllApprover{}, pm)
	require.NoError(t, err)
	decision := make(chan agent.Decision, 1)
	go func() {
		d, _ := app.Approve(voterCtx("okta|alice", "sre"), agent.ApprovalRequest{ToolName: "bash"})
		decision <- d
	}()
	return pm, emitter, waitForRequest(t, emitter), decision
}

func TestVote_ApproverGroupsGateWhoCounts(t *testing.T) {
	pm, emitter, token, decision := startGoverned(t, approval.Rule{
		Tool: "bash", NeededApprovals: 1, Timeout: 2 * time.Second, ApproverGroups: []string{"sre"},
	})

	res := pm.Vote(voterCtx("okta|bob", "dev"), token, "okta|bob", approval.VerdictApprove)
	assert.Equal(t, approval.VoteResultNotInApproverGroup, res.Kind, "a voter outside approver_groups must not count")
	res = pm.Vote(voterCtx("okta|dave"), token, "okta|dave", approval.VerdictDeny)
	assert.Equal(t, approval.VoteResultNotInApproverGroup, res.Kind, "nor deny on the group's behalf")

	res = pm.Vote(voterCtx("okta|carol", "dev", "sre"), token, "okta|carol", approval.VerdictApprove)
	assert.Equal(t, approval.VoteResultResolvedApprove, res.Kind)
	select {
	case d := <-decision:
		assert.Equal(t, agent.DecisionAllow, d)
	case <-time.After(time.Second):
		t.Fatal("approval did not resolve")
	}

	_, votes, _ := emitter.snapshot()
	require.Len(t, votes, 3)
	assert.False(t, votes[0].Counted, "an out-of-group vote is audited as not counted")
	assert.False(t, votes[1].Counted)
	assert.True(t, votes[2].Counted)
}

func TestVote_ApproverGroupsNeedTheVotersOwnIdentity(t *testing.T) {
	pm, _, token, _ := startGoverned(t, approval.Rule{
		Tool: "bash", NeededApprovals: 1, Timeout: 2 * time.Second, ApproverGroups: []string{"sre"},
	})
	// No identity on the ctx, and an identity that is not the voter.
	res := pm.Vote(context.Background(), token, "okta|carol", approval.VerdictApprove)
	assert.Equal(t, approval.VoteResultNotInApproverGroup, res.Kind)
	res = pm.Vote(voterCtx("okta|bob", "sre"), token, "okta|carol", approval.VerdictApprove)
	assert.Equal(t, approval.VoteResultNotInApproverGroup, res.Kind)
	assert.Contains(t, res.String(), "approver group")
}

func TestVote_WithoutApproverGroupsAnySubjectCounts(t *testing.T) {
	pm, _, token, _ := startGoverned(t, approval.Rule{Tool: "bash", NeededApprovals: 2, Timeout: 2 * time.Second})
	res := pm.Vote(context.Background(), token, "okta|bob", approval.VerdictApprove)
	assert.Equal(t, approval.VoteResultCounted, res.Kind)
}

func TestPendingRecord_ExposesApproverGroups(t *testing.T) {
	pm, _, token, _ := startGoverned(t, approval.Rule{
		Tool: "bash", NeededApprovals: 1, Timeout: 2 * time.Second, ApproverGroups: []string{"sre", "secops"},
	})
	rec, ok := pm.Lookup(token)
	require.True(t, ok)
	assert.Equal(t, []string{"sre", "secops"}, rec.ApproverGroups)
}
