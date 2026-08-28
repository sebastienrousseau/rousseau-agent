// Package agent contains the core domain types and orchestration loop for
// rousseau-agent. It depends only on interfaces exposed by internal/llm,
// internal/tools, and internal/state — never on their concrete
// implementations. This keeps the loop testable and the framework
// re-targetable.
package agent

import "errors"

var (
	// ErrEmptySession is returned when a session has no messages and cannot
	// be advanced.
	ErrEmptySession = errors.New("agent: empty session")

	// ErrMaxIterations is returned when the agent loop exceeds its
	// configured iteration budget without reaching an end-of-turn.
	ErrMaxIterations = errors.New("agent: iteration budget exhausted")

	// ErrToolNotFound is returned when the model requests a tool that is
	// not present in the registry.
	ErrToolNotFound = errors.New("agent: tool not found")

	// ErrToolRejected is returned when the user declines a pending tool
	// call.
	ErrToolRejected = errors.New("agent: tool call rejected by user")
)
