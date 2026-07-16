// Package subagent implements the sub-agent parallelism primitive
// (§8 of docs/IMPLEMENTATION_PLAN_2026_07_16.md). Callers hand
// Spawn a list of Tasks that each get run against a detached copy
// of the parent session with a bounded concurrency + per-task
// timeout + total-token budget, then aggregate the results back
// into a single [agent.Content] block the parent's model can consume
// on its next turn.
package subagent

import (
	"time"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
)

// Task describes one sub-agent invocation. Zero-value fields fall
// back to the defaults documented on each field.
type Task struct {
	// Prompt is the user-turn text handed to the sub-agent as its
	// last user message. Required.
	Prompt string
	// System overrides the parent's system prompt. Empty inherits.
	System string
	// Tools names the subset of the parent's tool registry the
	// sub-agent may invoke. Empty means "inherit every tool the
	// parent had access to" — most callers should scope this.
	Tools []string
	// MaxTurns caps how many completion round-trips the sub-agent
	// may run before being force-terminated. Zero uses 4.
	MaxTurns int
	// ProviderOverride, when non-nil, replaces the parent's Provider
	// for this task only. Useful for "run this cheap subtask against
	// haiku while the parent uses sonnet."
	ProviderOverride agent.Provider
	// Timeout bounds the per-task wall-clock duration. Zero uses
	// [Policy.PerTaskTimeout].
	Timeout time.Duration
}

// Result is the outcome of a single Task. Exactly one of FinalText /
// Err is non-empty on success paths; Err is non-nil when the
// sub-agent failed for any reason (budget, timeout, provider error,
// tool error).
type Result struct {
	// TaskIndex is the index of the Task in the input slice.
	// Preserved so callers can correlate results without a map.
	TaskIndex int
	// FinalText is the last assistant text block, if any.
	FinalText string
	// Turns is the number of provider round-trips that ran.
	Turns int
	// TokensIn / TokensOut sum across every provider call in this
	// sub-agent's execution.
	TokensIn  int
	TokensOut int
	// Duration is the wall-clock time from Spawn dispatch to
	// completion (or cancellation).
	Duration time.Duration
	// Err is non-nil when the task failed.
	Err error
}
