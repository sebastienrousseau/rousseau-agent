package agent

import (
	"context"
	"encoding/json"
)

// StreamEvent is a single progress event emitted while a Stream call
// runs. Providers deliver these on the events channel until the final
// Response arrives via StreamResult.
type StreamEvent struct {
	// Kind identifies the variant.
	Kind StreamEventKind
	// Delta carries the freshly-emitted assistant text fragment for
	// StreamTextDelta events; empty for other kinds.
	Delta string
	// Raw is the provider-native envelope, in case callers want to
	// consume fields that this Provider-agnostic shape does not map.
	Raw json.RawMessage
}

// StreamEventKind categorises stream events.
type StreamEventKind string

const (
	// StreamStart fires once when the provider acknowledges the request.
	StreamStart StreamEventKind = "start"
	// StreamTextDelta fires for each new fragment of assistant text.
	StreamTextDelta StreamEventKind = "text_delta"
	// StreamToolUse fires when the provider's tool loop invokes a tool
	// (informational — rousseau does not interpose on internal loops).
	StreamToolUse StreamEventKind = "tool_use"
	// StreamResult fires immediately before the final Response is
	// available.
	StreamResult StreamEventKind = "result"
	// StreamOther is a catch-all for provider events this shape does
	// not map explicitly.
	StreamOther StreamEventKind = "other"
)

// StreamReport is the terminal outcome of a Stream call, delivered on
// the report channel after the events channel closes.
type StreamReport struct {
	Response Response
	Err      error
}

// StreamingProvider is an optional interface a Provider implements
// when it can emit incremental progress. Callers detect support with a
// type assertion; the Provider interface itself only guarantees
// Complete because not every backend has a native streaming shape.
type StreamingProvider interface {
	Provider
	// Stream runs a completion in streaming mode. events is closed by
	// the provider when the stream completes; report delivers exactly
	// one StreamReport before it, too, is closed. Callers MUST drain
	// events to avoid leaking the provider's goroutine.
	Stream(ctx context.Context, req Request) (events <-chan StreamEvent, report <-chan StreamReport, err error)
}
