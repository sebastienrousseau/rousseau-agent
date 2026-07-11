package agent

import (
	"context"

	"github.com/sebastienrousseau/rousseau-agent/internal/tools"
)

// Request is a single completion request handed to a Provider.
type Request struct {
	// SessionID identifies the persistent conversation this request
	// belongs to. Providers that maintain server-side state (e.g. the
	// claudecli provider using --session-id) use this to correlate
	// turns. Providers that are stateless (e.g. the Anthropic API
	// direct client) ignore it.
	SessionID string
	System    string
	Messages  []Message
	Tools     []tools.Definition
}

// StopReason categorises why the model stopped generating.
type StopReason string

const (
	// StopEndTurn indicates the model finished its turn normally.
	StopEndTurn StopReason = "end_turn"
	// StopToolUse indicates the model requested one or more tool calls.
	StopToolUse StopReason = "tool_use"
	// StopMaxTokens indicates the response was truncated.
	StopMaxTokens StopReason = "max_tokens"
	// StopOther is a catch-all for unrecognised stop reasons.
	StopOther StopReason = "other"
)

// Response is a Provider's reply to a Request.
type Response struct {
	Message    Message
	StopReason StopReason
	Usage      Usage
}

// Usage records token counts for a single Response.
type Usage struct {
	InputTokens  int
	OutputTokens int
}

// Provider is the abstract completion contract. Implementations MUST be
// safe for concurrent use.
type Provider interface {
	// Name is a short, stable identifier ("anthropic", "openai", …).
	Name() string
	// Complete runs a single non-streaming completion.
	Complete(ctx context.Context, req Request) (Response, error)
}
