// Package approval implements multi-party approval workflows —
// the third and final slice of ROADMAP §2.9 (governance-
// advanced). Wraps the existing [agent.Approver] chain so a
// tool that needs "N distinct humans must OK this" is
// expressible without leaving the approver seam.
//
// # Model
//
// The operator lists tools that require multi-party approval,
// per rule: the minimum number of distinct approvers and an
// optional timeout. When the agent tries such a tool, the
// approver:
//
//  1. Mints a short unique token and records a pending entry.
//  2. Emits an audit record so external notification pipelines
//     (Slack, PagerDuty, email) can route the request to the
//     right rota — the daemon deliberately does NOT ship its
//     own notification transport for this. SIEMs are the
//     industry-standard fan-out point.
//  3. Blocks waiting for the pending entry to resolve.
//
// Approvers reply via chat: `/approve <token>` or
// `/deny <token>`. The router forwards the response to the
// [PendingManager]; when N distinct approvers vote allow, the
// waiter is unblocked with [agent.DecisionAllow]. Any single
// deny short-circuits to [agent.DecisionDeny].
//
// # Fail-CLOSED discipline
//
//   - Tool not in the rule set → defer to inner approver
//     (matches wrapWithRBAC's "only lock down what I named"
//     semantics).
//   - Anonymous requester (no SSO identity) → deny immediately.
//     Load-bearing: multi-party is meaningless without an
//     identity to gate on.
//   - Timeout expired → deny.
//   - Ctx cancellation → deny (agent stopped waiting; we
//     conservatively assume the operator wants safety).
//   - PendingManager unavailable → deny.
//   - Requester tries to self-approve → the vote is ignored
//     (audit-emitted but not counted toward N). Prevents the
//     obvious "I want to run terraform, so I /approve myself"
//     bypass.
package approval

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
	"github.com/sebastienrousseau/rousseau-agent/internal/auth/sso"
)

// Rule describes the multi-party requirement for one tool.
type Rule struct {
	// Tool is the model-facing tool name (case-insensitive
	// match against [agent.ApprovalRequest.ToolName]).
	Tool string
	// NeededApprovals is the count of DISTINCT approvers whose
	// /approve vote is required. Must be ≥ 1; a value of 1
	// still enforces the "not self-approve" rule so it isn't
	// pointless.
	NeededApprovals int
	// Timeout bounds how long the approver waits for the count
	// to be reached. Zero uses [DefaultTimeout].
	Timeout time.Duration
	// ApproverGroups, when set, restricts who may vote: only a voter
	// whose verified SSO identity carries one of these groups
	// (exact match on [sso.Identity.Groups]) counts toward
	// NeededApprovals or can deny. Empty lets any signed-in subject
	// other than the requester vote.
	ApproverGroups []string
}

// DefaultTimeout is the fallback timeout when a Rule leaves
// [Rule.Timeout] zero. Chosen so a busy on-call can grab a
// coffee before the request expires but not so long that
// forgotten requests linger for days.
const DefaultTimeout = 15 * time.Minute

// Verdict is what an approver's chat command produces.
type Verdict string

const (
	// VerdictApprove counts toward the N-approvers threshold.
	VerdictApprove Verdict = "approve"
	// VerdictDeny short-circuits the pending request to
	// [agent.DecisionDeny] regardless of accumulated approves.
	VerdictDeny Verdict = "deny"
)

// PendingRecord is one in-flight approval request.
type PendingRecord struct {
	Token     string
	Tool      string
	Requester string // SSO subject; never anonymous — Approver refuses to enqueue without one
	SessionID string
	// InputSHA256 is the hex SHA-256 of the exact tool input the vote
	// is about. /approve must quote its first DigestPrefixLen
	// characters, so a vote cannot land on a different call.
	InputSHA256 string
	// InputSummary is a short, human-readable form of the input
	// (path, command line, query) shown to voters.
	InputSummary string
	RequestedAt  time.Time
	ExpiresAt    time.Time
	NeededCount  int
	// ApproverGroups restricts voters to members of these SSO
	// groups; empty means any signed-in non-requester. See
	// [Rule.ApproverGroups].
	ApproverGroups []string

	// Votes records approver → verdict. Distinct-approver
	// guarantee: map keys deduplicate.
	Votes map[string]Verdict
	// resolved is closed when the record is resolved (either
	// count reached, denied, timed out, or ctx cancelled).
	resolved chan struct{}
	// finalVerdict is the ultimate outcome; only valid after
	// resolved is closed.
	finalVerdict Verdict
}

// AuditEmitter is the narrow slice of an audit sink the
// pending manager uses. Extracted so this package doesn't
// import internal/observability/audit_egress directly.
type AuditEmitter interface {
	// EmitApprovalRequest fires when a Record enters the
	// pending queue.
	EmitApprovalRequest(ctx context.Context, rec PendingRecord)
	// EmitApprovalVote fires on every approver /approve or
	// /deny (including self-approve attempts, which are
	// audited but rejected).
	EmitApprovalVote(ctx context.Context, token, approver string, verdict Verdict, counted bool)
	// EmitApprovalResolved fires when the record reaches a
	// final Verdict (allow / deny / timeout / ctx-cancel).
	EmitApprovalResolved(ctx context.Context, rec PendingRecord, verdict Verdict)
}

// NopEmitter is the fail-safe [AuditEmitter] — every method is
// a no-op. Used when audit egress isn't configured.
type NopEmitter struct{}

// EmitApprovalRequest satisfies [AuditEmitter].
func (NopEmitter) EmitApprovalRequest(context.Context, PendingRecord) {}

// EmitApprovalVote satisfies [AuditEmitter].
func (NopEmitter) EmitApprovalVote(context.Context, string, string, Verdict, bool) {}

// EmitApprovalResolved satisfies [AuditEmitter].
func (NopEmitter) EmitApprovalResolved(context.Context, PendingRecord, Verdict) {}

// PendingManager is the shared in-memory queue coordinating
// approvers and waiters. Router chat commands call [Vote];
// the [Approver] calls [Enqueue] + [Wait].
//
// The pilot is deliberately in-memory only — cross-restart /
// cross-daemon persistence lands as a small follow-up on the
// same interface once the mechanism has bake time.
type PendingManager struct {
	mu       sync.Mutex
	records  map[string]*PendingRecord
	emitter  AuditEmitter
	nowFn    func() time.Time // testability seam
	tokenGen func() string    // testability seam
}

// NewPendingManager returns a fresh manager. A nil emitter uses
// [NopEmitter] so callers can wire without audit egress.
func NewPendingManager(emitter AuditEmitter) *PendingManager {
	if emitter == nil {
		emitter = NopEmitter{}
	}
	return &PendingManager{
		records:  map[string]*PendingRecord{},
		emitter:  emitter,
		nowFn:    time.Now,
		tokenGen: randomToken,
	}
}

// randomToken produces a URL-safe 16-hex-char token — short
// enough to paste into a chat command, long enough that a
// stranger can't guess it. 64 bits of entropy from
// crypto/rand.
func randomToken() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// DigestPrefixLen is how many hex characters of InputSHA256 a voter
// quotes in /approve.
const DigestPrefixLen = 8

// PendingInput describes one request for votes.
type PendingInput struct {
	Tool      string
	Input     json.RawMessage
	Requester string
	SessionID string
	Needed    int
	Timeout   time.Duration
	// ApproverGroups restricts voters; see [Rule.ApproverGroups].
	ApproverGroups []string
}

// Enqueue records a pending request and returns it. The
// [Approver] passes the returned record to [Wait].
func (p *PendingManager) Enqueue(ctx context.Context, in PendingInput) *PendingRecord {
	timeout := in.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	sum := sha256.Sum256(in.Input)
	now := p.nowFn()
	rec := &PendingRecord{
		Token:          p.tokenGen(),
		Tool:           in.Tool,
		Requester:      in.Requester,
		SessionID:      in.SessionID,
		InputSHA256:    hex.EncodeToString(sum[:]),
		InputSummary:   agent.SummarizeToolInput(in.Tool, in.Input),
		RequestedAt:    now,
		ExpiresAt:      now.Add(timeout),
		NeededCount:    in.Needed,
		ApproverGroups: slices.Clone(in.ApproverGroups),
		Votes:          map[string]Verdict{},
		resolved:       make(chan struct{}),
	}
	p.mu.Lock()
	p.records[rec.Token] = rec
	p.mu.Unlock()
	p.emitter.EmitApprovalRequest(ctx, *rec)
	return rec
}

// Lookup returns a copy of the live record for token.
func (p *PendingManager) Lookup(token string) (PendingRecord, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	rec, ok := p.records[token]
	if !ok {
		return PendingRecord{}, false
	}
	return rec.snapshot(), true
}

// List returns copies of every live record, oldest first.
func (p *PendingManager) List() []PendingRecord {
	p.mu.Lock()
	out := make([]PendingRecord, 0, len(p.records))
	for _, rec := range p.records {
		out = append(out, rec.snapshot())
	}
	p.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].RequestedAt.Before(out[j].RequestedAt) })
	return out
}

// snapshot copies the exported fields; callers hold p.mu.
func (r *PendingRecord) snapshot() PendingRecord {
	cp := *r
	cp.Votes = make(map[string]Verdict, len(r.Votes))
	for k, v := range r.Votes {
		cp.Votes[k] = v
	}
	cp.resolved = nil
	return cp
}

// DigestMatches reports whether prefix is a DigestPrefixLen-or-longer
// prefix of the record's input digest.
func (r PendingRecord) DigestMatches(prefix string) bool {
	prefix = strings.ToLower(strings.TrimSpace(prefix))
	return len(prefix) >= DigestPrefixLen && strings.HasPrefix(r.InputSHA256, prefix)
}

// Wait blocks until the record resolves (count reached, denied,
// timed out, or ctx cancelled). Returns the final decision +
// reason string. Also removes the record from the manager on
// exit so a resolved token can't be re-voted.
func (p *PendingManager) Wait(ctx context.Context, rec *PendingRecord) (agent.Decision, string) {
	defer func() {
		p.mu.Lock()
		delete(p.records, rec.Token)
		p.mu.Unlock()
	}()

	timeoutDur := time.Until(rec.ExpiresAt)
	if timeoutDur <= 0 {
		return p.finalise(ctx, rec, VerdictDeny, "approval timed out")
	}
	timer := time.NewTimer(timeoutDur)
	defer timer.Stop()

	select {
	case <-rec.resolved:
		if rec.finalVerdict == VerdictApprove {
			return agent.DecisionAllow, ""
		}
		return agent.DecisionDeny, "governance: " + string(rec.finalVerdict)
	case <-timer.C:
		return p.finalise(ctx, rec, VerdictDeny, "approval timed out")
	case <-ctx.Done():
		return p.finalise(ctx, rec, VerdictDeny, "approval abandoned: "+ctx.Err().Error())
	}
}

// finalise closes the record's resolved channel (idempotent),
// stamps its final verdict, and emits the resolved audit event.
// Safe to call from either Wait's timeout branches or Vote's
// count-reached branch.
func (p *PendingManager) finalise(ctx context.Context, rec *PendingRecord, verdict Verdict, reason string) (agent.Decision, string) {
	p.mu.Lock()
	select {
	case <-rec.resolved:
		// already resolved by another path
	default:
		rec.finalVerdict = verdict
		close(rec.resolved)
	}
	p.mu.Unlock()
	p.emitter.EmitApprovalResolved(ctx, *rec, rec.finalVerdict)
	if rec.finalVerdict == VerdictApprove {
		return agent.DecisionAllow, ""
	}
	return agent.DecisionDeny, "governance: " + reason
}

// Vote records one approver's verdict against token. Returns
// the outcome codes so the caller (router chat command) can
// reply usefully to the approver.
//
// Rules:
//   - unknown token → VoteResultUnknownToken
//   - approver is the original requester → VoteResultSelfApproveRejected
//   - rule has approver groups and ctx's verified identity is not the
//     approver or is in none of them → VoteResultNotInApproverGroup
//   - already voted → VoteResultDuplicateVote (idempotent)
//   - deny → VoteResultDenied; record resolved immediately
//   - approve; count not yet reached → VoteResultCounted (returns current/needed)
//   - approve; count reached → VoteResultResolvedApprove
func (p *PendingManager) Vote(ctx context.Context, token, approver string, verdict Verdict) VoteResult {
	if approver == "" {
		return VoteResult{Kind: VoteResultAnonymousRejected}
	}
	p.mu.Lock()
	rec, ok := p.records[token]
	if !ok {
		p.mu.Unlock()
		return VoteResult{Kind: VoteResultUnknownToken}
	}
	// The map access + resolved check + Votes mutation all
	// need the mutex; unlock only at the end of the branch
	// that doesn't reach the finalise path.
	if kind, rejected := rec.rejectVote(ctx, approver); rejected {
		p.mu.Unlock()
		if kind.audited() {
			p.emitter.EmitApprovalVote(ctx, token, approver, verdict, false)
		}
		return VoteResult{Kind: kind}
	}
	rec.Votes[approver] = verdict
	countedApprovals := rec.approvals()
	p.mu.Unlock()

	p.emitter.EmitApprovalVote(ctx, token, approver, verdict, true)

	if verdict == VerdictDeny {
		p.finalise(ctx, rec, VerdictDeny, "denied by "+approver)
		return VoteResult{Kind: VoteResultDenied}
	}
	if countedApprovals >= rec.NeededCount {
		p.finalise(ctx, rec, VerdictApprove, "")
		return VoteResult{Kind: VoteResultResolvedApprove, Approvals: countedApprovals, Needed: rec.NeededCount}
	}
	return VoteResult{Kind: VoteResultCounted, Approvals: countedApprovals, Needed: rec.NeededCount}
}

// rejectVote reports why approver may not vote on r, if they may
// not. Caller holds p.mu.
func (r *PendingRecord) rejectVote(ctx context.Context, approver string) (VoteResultKind, bool) {
	select {
	case <-r.resolved:
		return VoteResultAlreadyResolved, true
	default:
	}
	if approver == r.Requester {
		return VoteResultSelfApproveRejected, true
	}
	if !r.mayVote(ctx, approver) {
		return VoteResultNotInApproverGroup, true
	}
	if _, already := r.Votes[approver]; already {
		return VoteResultDuplicateVote, true
	}
	return 0, false
}

// mayVote applies the approver-group restriction: with groups set,
// the voter must be the verified identity on ctx and carry one of
// them. The voter string alone is not trusted for group membership.
func (r *PendingRecord) mayVote(ctx context.Context, approver string) bool {
	if len(r.ApproverGroups) == 0 {
		return true
	}
	id, ok := sso.IdentityFromContext(ctx)
	if !ok || id.Subject != approver {
		return false
	}
	return slices.ContainsFunc(id.Groups, func(g string) bool { return slices.Contains(r.ApproverGroups, g) })
}

// approvals counts the approve votes. Caller holds p.mu.
func (r *PendingRecord) approvals() int {
	n := 0
	for _, v := range r.Votes {
		if v == VerdictApprove {
			n++
		}
	}
	return n
}

// VoteResultKind is the enum of Vote outcomes.
type VoteResultKind int

// audited reports whether a rejected vote of this kind is still
// written to the audit trail (as not counted): attempts by the wrong
// person are worth a record, duplicates and late votes are not.
func (k VoteResultKind) audited() bool {
	return k == VoteResultSelfApproveRejected || k == VoteResultNotInApproverGroup
}

// VoteResultKind constants.
const (
	// VoteResultUnknownToken means the token doesn't match a
	// live pending record. Common cause: it already resolved.
	VoteResultUnknownToken VoteResultKind = iota + 1
	// VoteResultAnonymousRejected means the caller had no
	// authenticated identity — /approve without /login isn't
	// meaningful.
	VoteResultAnonymousRejected
	// VoteResultSelfApproveRejected means the approver IS the
	// original requester. Audited but not counted.
	VoteResultSelfApproveRejected
	// VoteResultDuplicateVote means the approver already voted
	// on this token. Idempotent — no state change.
	VoteResultDuplicateVote
	// VoteResultCounted means the vote counted but the
	// threshold isn't yet reached.
	VoteResultCounted
	// VoteResultResolvedApprove means the vote pushed the
	// count past the threshold; waiter unblocked with allow.
	VoteResultResolvedApprove
	// VoteResultDenied means a /deny short-circuited the
	// pending request.
	VoteResultDenied
	// VoteResultAlreadyResolved means the record resolved
	// before this vote landed (approver's chat message raced
	// the timeout, for instance).
	VoteResultAlreadyResolved
	// VoteResultNotInApproverGroup means the rule names approver
	// groups and the voter's verified identity is in none of them
	// (or is not the voter's). Audited but not counted.
	VoteResultNotInApproverGroup
)

// VoteResult carries the Vote outcome + progress counters.
type VoteResult struct {
	Kind VoteResultKind
	// Approvals is the current count of allow-votes (only
	// meaningful for Counted / ResolvedApprove).
	Approvals int
	// Needed is the required count. Same lifetime as Approvals.
	Needed int
}

// String is a compact human-readable form for chat replies.
func (r VoteResult) String() string {
	switch r.Kind {
	case VoteResultUnknownToken:
		return "unknown or already-resolved approval token"
	case VoteResultAnonymousRejected:
		return "sign in via /login before voting"
	case VoteResultSelfApproveRejected:
		return "you cannot approve your own request"
	case VoteResultDuplicateVote:
		return "you already voted on this token"
	case VoteResultCounted:
		return fmt.Sprintf("recorded — %d of %d approvals", r.Approvals, r.Needed)
	case VoteResultResolvedApprove:
		return fmt.Sprintf("approved — %d of %d met", r.Approvals, r.Needed)
	case VoteResultDenied:
		return "denied"
	case VoteResultAlreadyResolved:
		return "already resolved"
	case VoteResultNotInApproverGroup:
		return "you are not in an approver group for this request"
	default:
		return "unknown result"
	}
}

// Approver is the multi-party wrapper. Constructed via
// [NewApprover].
type Approver struct {
	rules   map[string]Rule // tool (lower) → rule
	inner   agent.Approver
	pending *PendingManager
}

// NewApprover wraps inner with a multi-party gate. A nil inner
// uses [agent.AllowAllApprover]; a nil manager returns nil so
// misuse fails at the call site.
func NewApprover(rules []Rule, inner agent.Approver, pending *PendingManager) (*Approver, error) {
	if pending == nil {
		return nil, errors.New("approval: PendingManager is required")
	}
	if inner == nil {
		inner = agent.AllowAllApprover{}
	}
	byTool := make(map[string]Rule, len(rules))
	for _, r := range rules {
		if strings.TrimSpace(r.Tool) == "" {
			return nil, errors.New("approval: rule with empty tool name")
		}
		if r.NeededApprovals < 1 {
			return nil, fmt.Errorf("approval: tool %q needs NeededApprovals >= 1", r.Tool)
		}
		byTool[strings.ToLower(strings.TrimSpace(r.Tool))] = r
	}
	return &Approver{rules: byTool, inner: inner, pending: pending}, nil
}

// Approve satisfies [agent.Approver]. Blocks until the pending
// entry resolves or the ctx / timeout fires.
func (a *Approver) Approve(ctx context.Context, req agent.ApprovalRequest) (agent.Decision, string) {
	// Policy first: a group, Rego, risk or pattern deny ends the
	// request before anyone is asked to vote. An inner allow does not
	// skip the vote for a covered tool.
	if d, why := a.inner.Approve(ctx, req); d == agent.DecisionDeny {
		return d, why
	}
	rule, covered := a.rules[strings.ToLower(req.ToolName)]
	if !covered {
		return agent.DecisionAllow, ""
	}
	id, ok := sso.IdentityFromContext(ctx)
	if !ok || id.Subject == "" {
		return agent.DecisionDeny, "governance: multi-party approval requires an authenticated requester (sign in via /login)"
	}
	rec := a.pending.Enqueue(ctx, PendingInput{
		Tool: req.ToolName, Input: req.Input, Requester: id.Subject,
		SessionID: req.SessionID, Needed: rule.NeededApprovals, Timeout: rule.Timeout,
		ApproverGroups: rule.ApproverGroups,
	})
	return a.pending.Wait(ctx, rec)
}

// Compile-time interface satisfaction.
var _ agent.Approver = (*Approver)(nil)
