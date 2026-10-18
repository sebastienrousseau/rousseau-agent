package config

import (
	"time"
)

// AgentConfig configures the agent loop.
type AgentConfig struct {
	SystemPrompt  string            `mapstructure:"system_prompt"`
	MaxIterations int               `mapstructure:"max_iterations"`
	Approver      ApproverConfig    `mapstructure:"approver"`
	Compression   CompressionConfig `mapstructure:"compression"`
	SkillsDir     string            `mapstructure:"skills_dir"`
	// SkillsMode selects the skills loader. Two values today:
	//
	//   "" or "legacy" (default) — the pre-Phase-2.1 flat-file
	//     format. SkillsDir is scanned non-recursively for *.md
	//     files; each file's `triggers: [...]` frontmatter drives
	//     keyword activation. Kept as default for backwards
	//     compatibility while callers migrate their skill files.
	//
	//   "spec" — the agentskills.io three-tier progressive-
	//     disclosure model. SkillsDir is walked for subdirectories
	//     containing SKILL.md; each skill's name + description are
	//     injected as a <available_skills> catalog into the system
	//     prompt, and the model loads bodies lazily via its Read
	//     tool on demand. Roughly 90-95% token reduction on
	//     skills-heavy installations. See docs/skills.md for the
	//     migration guide.
	//
	// SkillBundles (enterprise) is currently legacy-only; a
	// spec-mode bundle path lands with the metadata.x-rousseau-
	// signature verification in a subsequent commit.
	SkillsMode string `mapstructure:"skills_mode"`
	// SkillBundles configures the enterprise-only
	// cryptographically-signed skill bundle loader (see
	// internal/skills/bundle). Zero value leaves the loader
	// off — plain-markdown SkillsDir behaviour is unchanged.
	// Requires FeatureGovernanceAdvanced. Legacy-mode only for
	// now; spec-mode ignores this with a WARN log.
	SkillBundles SkillBundlesConfig `mapstructure:"skill_bundles"`
	// EnableConfidenceElicitation appends a one-line instruction
	// to every system prompt asking the model to close each turn
	// with <confidence>0.NN</confidence>. Agent.Turn parses the
	// tag, pairs it with the outcome, and emits a Predictability
	// "pair" sample the reliability aggregator's Brier / ECE /
	// AUROC calculations consume.
	//
	// Costs ~40 tokens per turn (the addendum) plus ~10 tokens
	// per reply (the closing tag). Turn on when you want real
	// live Predictability numbers in `rousseau reliability`;
	// leave off for token-sensitive deployments.
	EnableConfidenceElicitation bool `mapstructure:"enable_confidence_elicitation"`
	// SessionIdleTimeout starts a fresh chat session when a sender's
	// current one has been idle longer than this, so a short reply
	// days later is not taken as an answer to a stale question. The
	// reply names the previous session for /resume. Default 12h;
	// set to 0 to always continue the existing session.
	SessionIdleTimeout time.Duration `mapstructure:"session_idle_timeout"`
	// TurnTimeout bounds one chat turn end to end. On expiry the
	// provider subprocess is stopped and the sender is told the turn
	// was cut short. Default 30m; 0 disables the limit.
	TurnTimeout time.Duration `mapstructure:"turn_timeout"`
	// ResumeInterrupted continues a turn a restart cut off from its
	// last checkpoint and delivers the reply, instead of only telling
	// the sender what had run. Off by default: resuming runs the model,
	// and possibly more tools, without the sender asking again.
	ResumeInterrupted bool `mapstructure:"resume_interrupted"`
	// ToolTimeout bounds one tool execution inside a turn so a hung
	// bash or MCP call cannot consume the whole turn budget. Default
	// 10m.
	ToolTimeout time.Duration `mapstructure:"tool_timeout"`
	// MaxToolOutputBytes caps the bytes of one tool's output handed
	// back to the model; the rest is cut with a marker. Default 65536.
	MaxToolOutputBytes int `mapstructure:"max_tool_output_bytes"`
	// MaxConcurrentTurns caps agent turns running at once in this
	// daemon; further turns queue. Each claudecli turn is a separate
	// claude process. Default 4; 0 means unlimited.
	MaxConcurrentTurns int `mapstructure:"max_concurrent_turns"`
}

// SkillBundlesConfig is the operator-facing view of the
// signed-bundle loader. Zero value = disabled.
type SkillBundlesConfig struct {
	// Dir is the directory the loader scans for
	// *.skill.json files. Empty leaves the loader off.
	Dir string `mapstructure:"dir"`
	// TrustedPublisherKeys is the base64-encoded Ed25519
	// public key allow-list. A bundle whose
	// signature.public_key isn't in this list is rejected —
	// the trust root is the operator, not implicit.
	// Standard base64 (matches the same encoding the license
	// package uses for embedded keys).
	TrustedPublisherKeys []string `mapstructure:"trusted_publisher_keys"`
	// Strict, when true, elevates verification failures to
	// ERROR log level. Verification never falls through to
	// "load anyway" — a broken bundle simply isn't loaded.
	Strict bool `mapstructure:"strict"`
}

// CompressionConfig configures session compression. Disabled by
// default because a long-running daemon on a subscription-tier claude
// account rarely needs it — turn it on when running against
// pay-per-token providers.
type CompressionConfig struct {
	// Enabled toggles the LLM-backed compressor. When false, the
	// Agent uses NoopCompressor.
	Enabled bool `mapstructure:"enabled"`
	// TriggerMessages is the message count above which compression
	// engages. Zero uses the default (60).
	TriggerMessages int `mapstructure:"trigger_messages"`
	// TriggerTokens is the estimated prompt size above which
	// compression engages regardless of message count. Zero uses the
	// default (120000, under every current model's context window).
	TriggerTokens int `mapstructure:"trigger_tokens"`
	// KeepRecent is how many recent messages to preserve verbatim.
	// Zero uses the default (8).
	KeepRecent int `mapstructure:"keep_recent"`
	// Prompt overrides the default summarisation instruction.
	Prompt string `mapstructure:"prompt"`
}
