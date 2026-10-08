package model

import "context"

// CostRecorder is the seam the agent loop uses to persist per-call
// cost telemetry. Implementations must be safe for concurrent use.
// Errors returned from Record are logged at Warn but never abort the
// agent loop — cost telemetry is best-effort observability, not a
// correctness dependency.
type CostRecorder interface {
	Record(ctx context.Context, r CostEvent) error
}

// CostEvent is what the agent loop hands to a CostRecorder after
// every completion. Provider + Model may be empty for older provider
// implementations that don't populate them.
type CostEvent struct {
	SessionID string
	Provider  string
	Model     string
	Usage     Usage
}

// SkillsProvider returns text spliced into the system prompt for a
// given session. Implementations typically look at the last user
// message and select relevant skills.
type SkillsProvider interface {
	SystemAppendix(s *Session) string
}

// RecallProvider looks up snippets from prior sessions relevant to the
// current user message and returns them as a system-prompt appendix.
// It is the cross-session analogue of SkillsProvider.
type RecallProvider interface {
	// SystemAppendix inspects s and returns text to append to the base
	// system prompt. Empty return leaves the prompt untouched.
	SystemAppendix(ctx context.Context, s *Session) string
}

// SearchHit is the shape a recall backend returns per matched session.
type SearchHit struct {
	SessionID string
	Title     string
	Snippet   string
}

// RecallSearcher is the narrow surface the FTS-backed recall provider
// depends on. Stores implement it; the agent loop's FTSRecall consumes
// it. Defined here so neither side imports the other.
type RecallSearcher interface {
	// Search returns hits only from sessions whose Sender equals
	// sender exactly (the empty sender included). It must never
	// return another sender's sessions: recall output goes into the
	// system prompt of sender's own conversation.
	Search(ctx context.Context, sender, query string, limit int) ([]SearchHit, error)
}
