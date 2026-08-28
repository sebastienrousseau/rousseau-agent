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
	// CacheableMessages hints how many *leading* messages of this
	// Request are stable enough for the provider to mark for a
	// server-side prompt cache. Zero disables the hint (caller has no
	// opinion — provider default applies). Providers that do not
	// implement caching ignore this value; providers that do
	// (Anthropic's ephemeral cache) mark the last CacheableMessages
	// blocks with cache_control.
	//
	// Compressor implementations set this to len(recentMessages) - 1
	// after a rewrite so the summary block hits the cache on the very
	// next turn.
	CacheableMessages int
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
	// Model is the concrete model identifier the provider used for
	// this completion. Providers that support multiple models (or that
	// route internally per-request) set this so callers can attribute
	// cost + telemetry to the right SKU. Empty means "not reported"
	// (older provider implementations before this field was added).
	Model string
}

// Usage records token counts for a single Response. Cache fields
// are populated by providers that support prompt caching (Anthropic
// today; other providers leave them zero). The cache metrics let
// callers compute hit-rate as CacheReadInputTokens / (CacheReadInputTokens
// + CacheCreationInputTokens) — a >70% hit ratio on a steady-state
// daemon is a good sign the system-prompt cache breakpoints are set
// correctly.
type Usage struct {
	InputTokens              int
	OutputTokens             int
	CacheCreationInputTokens int
	CacheReadInputTokens     int
	// CacheCreationEphemeral1h and CacheCreationEphemeral5m break the
	// cache-creation number down by TTL bucket. Sum of the two equals
	// CacheCreationInputTokens. Zero when the provider does not report
	// per-TTL creation counts.
	CacheCreationEphemeral1h int
	CacheCreationEphemeral5m int
}

// Provider is the abstract completion contract. Implementations MUST be
// safe for concurrent use.
type Provider interface {
	// Name is a short, stable identifier ("anthropic", "openai", …).
	Name() string
	// Complete runs a single non-streaming completion.
	Complete(ctx context.Context, req Request) (Response, error)
}
