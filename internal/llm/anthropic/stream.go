package anthropic

import (
	"context"
	"errors"
	"fmt"

	sdk "github.com/anthropics/anthropic-sdk-go"
	sdkssestream "github.com/anthropics/anthropic-sdk-go/packages/ssestream"

	"github.com/sebastienrousseau/rousseau-agent/internal/model"
)

// Stream runs a streaming completion via the Messages API. It emits
// model.StreamEvents for provider-observed progress and finalises with
// a StreamReport carrying the assembled Response.
//
// The SDK's streaming iterator hands us delta events (text_delta,
// input_json_delta, message_delta, message_stop). We aggregate text
// deltas into a running assistant message and surface them as
// model.StreamTextDelta events for the caller.
func (p *Provider) Stream(ctx context.Context, req model.Request) (<-chan model.StreamEvent, <-chan model.StreamReport, error) {
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
		sys := sdk.TextBlockParam{Text: req.System}
		if req.CacheableMessages > 0 {
			sys.CacheControl = cacheEphemeral1h
		}
		params.System = []sdk.TextBlockParam{sys}
	}
	if len(req.Tools) > 0 {
		params.Tools = toSDKTools(req.Tools, req.CacheableMessages > 0)
	}
	applyCacheMarkers(params.Messages, req.CacheableMessages)

	stream := p.client.Messages.NewStreaming(ctx, params)

	events := make(chan model.StreamEvent, 16)
	report := make(chan model.StreamReport, 1)

	go func() {
		defer close(events)
		defer close(report)
		resp, sErr := consumeStream(stream, events)
		if closeErr := stream.Close(); sErr == nil && closeErr != nil {
			sErr = fmt.Errorf("anthropic: close stream: %w", closeErr)
		}
		report <- model.StreamReport{Response: resp, Err: sErr}
	}()
	return events, report, nil
}

// consumeStream advances the SDK iterator, emits model.StreamEvent per
// SSE payload, and assembles the terminal model.Response.
func consumeStream(stream *sdkssestream.Stream[sdk.MessageStreamEventUnion], events chan<- model.StreamEvent) (model.Response, error) {
	var (
		message   sdk.Message
		sentStart bool
	)
	for stream.Next() {
		evt := stream.Current()
		if !sentStart {
			events <- model.StreamEvent{Kind: model.StreamStart}
			sentStart = true
		}

		if err := message.Accumulate(evt); err != nil {
			return model.Response{}, fmt.Errorf("anthropic: accumulate: %w", err)
		}

		switch payload := evt.AsAny().(type) {
		case sdk.ContentBlockDeltaEvent:
			text := extractDeltaText(payload)
			if text != "" {
				events <- model.StreamEvent{Kind: model.StreamTextDelta, Delta: text}
			}
		case sdk.ContentBlockStartEvent:
			if isToolUseStart(payload) {
				events <- model.StreamEvent{Kind: model.StreamToolUse}
			}
		}
	}
	if err := stream.Err(); err != nil {
		return model.Response{}, wrapAPIError("anthropic: stream", err)
	}

	assistant, err := fromAssembledMessage(&message)
	if err != nil {
		return model.Response{}, err
	}
	events <- model.StreamEvent{Kind: model.StreamResult}

	return model.Response{
		Message:    assistant,
		StopReason: mapStopReason(string(message.StopReason)),
		Usage: model.Usage{
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
func fromAssembledMessage(m *sdk.Message) (model.Message, error) {
	if m == nil {
		return model.Message{}, errors.New("anthropic: nil assembled message")
	}
	// The Complete path already knows how to convert; delegate.
	return fromSDKResponse(m)
}

// Compile-time check that Provider satisfies model.StreamingProvider.
var _ model.StreamingProvider = (*Provider)(nil)
