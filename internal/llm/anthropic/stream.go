package anthropic

import (
	"context"
	"errors"
	"fmt"

	sdk "github.com/anthropics/anthropic-sdk-go"
	sdkssestream "github.com/anthropics/anthropic-sdk-go/packages/ssestream"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
)

// Stream runs a streaming completion via the Messages API. It emits
// agent.StreamEvents for provider-observed progress and finalises with
// a StreamReport carrying the assembled Response.
//
// The SDK's streaming iterator hands us delta events (text_delta,
// input_json_delta, message_delta, message_stop). We aggregate text
// deltas into a running assistant message and surface them as
// agent.StreamTextDelta events for the caller.
func (p *Provider) Stream(ctx context.Context, req agent.Request) (<-chan agent.StreamEvent, <-chan agent.StreamReport, error) {
	msgs, err := toSDKMessages(req.Messages)
	if err != nil {
		return nil, nil, err
	}
	params := sdk.MessageNewParams{
		Model:     p.cfg.Model,
		MaxTokens: p.cfg.MaxTokens,
		Messages:  msgs,
	}
	if req.System != "" {
		params.System = []sdk.TextBlockParam{{Text: req.System}}
	}
	if len(req.Tools) > 0 {
		params.Tools = toSDKTools(req.Tools)
	}

	stream := p.client.Messages.NewStreaming(ctx, params)

	events := make(chan agent.StreamEvent, 16)
	report := make(chan agent.StreamReport, 1)

	go func() {
		defer close(events)
		defer close(report)
		resp, sErr := consumeStream(stream, events)
		if closeErr := stream.Close(); sErr == nil && closeErr != nil {
			sErr = fmt.Errorf("anthropic: close stream: %w", closeErr)
		}
		report <- agent.StreamReport{Response: resp, Err: sErr}
	}()
	return events, report, nil
}

// consumeStream advances the SDK iterator, emits agent.StreamEvent per
// SSE payload, and assembles the terminal agent.Response.
func consumeStream(stream *sdkssestream.Stream[sdk.MessageStreamEventUnion], events chan<- agent.StreamEvent) (agent.Response, error) {
	var (
		message   sdk.Message
		sentStart bool
	)
	for stream.Next() {
		evt := stream.Current()
		if !sentStart {
			events <- agent.StreamEvent{Kind: agent.StreamStart}
			sentStart = true
		}

		if err := message.Accumulate(evt); err != nil {
			return agent.Response{}, fmt.Errorf("anthropic: accumulate: %w", err)
		}

		switch payload := evt.AsAny().(type) {
		case sdk.ContentBlockDeltaEvent:
			text := extractDeltaText(payload)
			if text != "" {
				events <- agent.StreamEvent{Kind: agent.StreamTextDelta, Delta: text}
			}
		case sdk.ContentBlockStartEvent:
			if isToolUseStart(payload) {
				events <- agent.StreamEvent{Kind: agent.StreamToolUse}
			}
		}
	}
	if err := stream.Err(); err != nil {
		return agent.Response{}, fmt.Errorf("anthropic: stream: %w", err)
	}

	assistant, err := fromAssembledMessage(&message)
	if err != nil {
		return agent.Response{}, err
	}
	events <- agent.StreamEvent{Kind: agent.StreamResult}

	return agent.Response{
		Message:    assistant,
		StopReason: mapStopReason(string(message.StopReason)),
		Usage: agent.Usage{
			InputTokens:  int(message.Usage.InputTokens),
			OutputTokens: int(message.Usage.OutputTokens),
		},
	}, nil
}

// extractDeltaText returns the text carried by a ContentBlockDeltaEvent
// or "" if the delta is not a text delta.
func extractDeltaText(evt sdk.ContentBlockDeltaEvent) string {
	switch d := evt.Delta.AsAny().(type) {
	case sdk.TextDelta:
		return d.Text
	default:
		return ""
	}
}

// isToolUseStart reports whether a content_block_start event opened a
// tool_use block. The SDK's typed accessors don't expose a boolean
// directly; we introspect via the union.
func isToolUseStart(evt sdk.ContentBlockStartEvent) bool {
	_, ok := evt.ContentBlock.AsAny().(sdk.ToolUseBlock)
	return ok
}

// fromAssembledMessage mirrors fromSDKResponse but works on an
// already-accumulated message rather than a Complete response.
func fromAssembledMessage(m *sdk.Message) (agent.Message, error) {
	if m == nil {
		return agent.Message{}, errors.New("anthropic: nil assembled message")
	}
	// The Complete path already knows how to convert; delegate.
	return fromSDKResponse(m)
}

// Compile-time check that Provider satisfies agent.StreamingProvider.
var _ agent.StreamingProvider = (*Provider)(nil)
