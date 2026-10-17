package config

import (
	"time"
)

// ApproverConfig picks and configures the tool-call approval policy.
//
// mode:
//   - "allow_all" (default): every tool call runs, except outbound
//     integration tools in the daemon unless AllowOutbound is set.
//     Suitable when the provider is claudecli, which handles its own
//     approvals.
//   - "deny_all": block every tool call. Useful as a smoke test or
//     when running a read-only inspection session.
//   - "pattern":  applies Allow / Deny regex rules; deny wins over
//     allow; unmatched requests fall back to `default`.
type ApproverConfig struct {
	Mode    string         `mapstructure:"mode"`
	Reason  string         `mapstructure:"reason"`
	Default string         `mapstructure:"default"` // "allow" or "deny" for pattern mode
	Allow   []PatternEntry `mapstructure:"allow"`
	Deny    []PatternEntry `mapstructure:"deny"`
	// AllowOutbound lets the allow_all mode (including the empty
	// default) run outbound integration tools — the ones that send
	// data to a third party or change third-party state, such as
	// gmail_send, slack_post_message, github_create_issue and every
	// Composio action. Off by default: under allow_all the daemon
	// denies those tools, so a prompt-injected turn cannot read data
	// and then send it out. Setting it true restores the pre-L-32
	// behaviour. Ignored in the other modes, whose rules already
	// decide which tools run.
	AllowOutbound bool `mapstructure:"allow_outbound"`
	// RBAC wraps the mode-selected approver with a group-based
	// gate. Zero value leaves RBAC off — the mode-selected
	// approver runs alone. Activates only when the licence
	// unlocks FeatureGovernanceAdvanced; without the licence the
	// rules are ignored and the daemon logs an INFO on boot.
	RBAC RBACConfig `mapstructure:"rbac"`
	// OPA wraps the approver chain (below RBAC) with a Rego-
	// per-tool-call policy. Zero value leaves OPA off. Same
	// licence gate as RBAC. Composition order:
	//   inner ← RBAC ← OPA
	// so both layers can veto; a broken policy falls back to
	// inner without wrapping.
	OPA OPAConfig `mapstructure:"opa"`
	// MultiParty wraps the approver chain (outermost) with an
	// N-approvers-required layer. Rules that name a tool block
	// its execution until N distinct SSO-authenticated
	// approvers reply /approve via chat. Zero value leaves the
	// layer off. Same licence gate. Composition order:
	//   inner ← RBAC ← OPA ← MultiParty
	// so a request must pass all three before the mode-selected
	// (pattern / TUI) approver has its final say.
	MultiParty MultiPartyConfig `mapstructure:"multi_party"`
	// Risk adds a model-scored check after the deterministic layers:
	// for the listed tools, a call they allow is judged by the model
	// and denied when it is confidently risky. Same licence gate.
	Risk RiskConfig `mapstructure:"risk"`
}

// RiskConfig configures the risk-scored approver (agent.RiskApprover).
type RiskConfig struct {
	Enabled bool `mapstructure:"enabled"`
	// Tools to score, e.g. [bash, write, edit]; empty scores every
	// tool (one extra model call per tool call).
	Tools []string `mapstructure:"tools"`
	// Threshold is the confidence at or above which a "risky" verdict
	// denies the call. Default 0.8.
	Threshold float64 `mapstructure:"threshold"`
	// Model overrides the judge model on the claudecli provider (a
	// small fast model keeps the added latency low). Empty uses the
	// provider's model.
	Model string `mapstructure:"model"`
	// FailOpen lets calls through when the judge cannot answer. Off by
	// default: a judge failure blocks the scored call.
	FailOpen bool `mapstructure:"fail_open"`
	// Timeout bounds one judgement. Default 60s.
	Timeout time.Duration `mapstructure:"timeout"`
}

// RBACConfig configures the group-based RBAC wrapper.
type RBACConfig struct {
	// Rules maps a tool name to the SSO groups permitted to
	// invoke it. A tool not listed here bypasses the RBAC layer
	// — only explicitly-locked tools filter. See
	// internal/agent/rbac for semantics.
	Rules []RBACRule `mapstructure:"rules"`
}

// OPAConfig configures the Rego-per-tool-call approver (see
// internal/agent/opa). Zero value leaves the OPA layer off —
// the mode-selected + RBAC-wrapped approver runs alone.
type OPAConfig struct {
	// PolicyFile is the on-disk path to the Rego module the
	// daemon evaluates on every tool call. Read at boot; a
	// missing file or parse error is fatal to the layer (WARN
	// + wrap-skipped, matching wrapWithRBAC's failure mode).
	PolicyFile string `mapstructure:"policy_file"`
	// Query overrides the default Rego query
	// (`data.rousseau.authz`). Rarely needed.
	Query string `mapstructure:"query"`
}

// MultiPartyConfig configures the N-approvers-required approver
// (see internal/agent/approval). Zero value leaves the layer
// off. Requires FeatureGovernanceAdvanced.
type MultiPartyConfig struct {
	// Rules list the tools that need multi-party approval.
	Rules []MultiPartyRule `mapstructure:"rules"`
}

// MultiPartyRule pins one tool to a minimum-approvers threshold
// + timeout.
type MultiPartyRule struct {
	// Tool is the model-facing tool name.
	Tool string `mapstructure:"tool"`
	// NeededApprovals is the count of DISTINCT non-self
	// approvers required to allow the call.
	NeededApprovals int `mapstructure:"needed_approvals"`
	// Timeout bounds how long the pending request lives. Zero
	// uses approval.DefaultTimeout (15m).
	Timeout time.Duration `mapstructure:"timeout"`
	// ApproverGroups (approver_groups) restricts who may vote on this
	// tool's requests to members of these SSO groups (exact match on
	// the verified identity's groups). A vote from anyone else is
	// audited and ignored, approve or deny. Empty lets any signed-in
	// subject other than the requester vote.
	ApproverGroups []string `mapstructure:"approver_groups"`
}

// RBACRule mirrors [rbac.Rule] but keeps mapstructure tags out
// of the domain package.
type RBACRule struct {
	Tool          string   `mapstructure:"tool"`
	AllowedGroups []string `mapstructure:"allowed_groups"`
}

// PatternEntry mirrors agent.PatternRule but decouples config from the
// agent package so importers don't need both.
//
// Tool is compared exactly (case-sensitive). With Field empty, Match is
// an unanchored regex over the canonical input JSON. With Field set,
// Match is anchored (^(?:match)$) and tested against that top-level
// string field of the input — the form to use for allow rules, since
// an unanchored allow for "git status" also allows "git status; …".
type PatternEntry struct {
	Tool  string `mapstructure:"tool"`
	Match string `mapstructure:"match"`
	Field string `mapstructure:"field"`
}
