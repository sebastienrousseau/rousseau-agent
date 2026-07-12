package agent

import (
	"context"
	"fmt"
	"log/slog"
)

// TurnStream is the streaming twin of Turn. It behaves identically to
// Turn — same compression pass, same tool loop, same iteration budget —
// but each provider round-trip is streamed via the optional
// StreamingProvider interface (falling back to Complete when the
// provider does not implement it).
//
// events receives every StreamEvent the provider emits. TurnStream is
// responsible for closing events before returning. If the caller only
// wants text deltas they can discard everything else with a switch on
// StreamEvent.Kind.
//
// The Session is mutated in place; the final assistant Message is
// returned exactly as by Turn.
func (a *Agent) TurnStream(ctx context.Context, s *Session, events chan<- StreamEvent) (Message, error) {
	defer close(events)

	if len(s.Messages) == 0 {
		return Message{}, ErrEmptySession
	}
	if changed, err := a.opts.Compressor.Compress(ctx, s); err != nil {
		a.logger.Warn("agent.compress_failed", slog.String("err", err.Error()))
	} else if changed {
		a.logger.Info("agent.compressed", slog.Int("messages", len(s.Messages)))
	}

	toolDefs := a.registry.Definitions()
	streamer, canStream := a.provider.(StreamingProvider)

	for i := 0; i < a.opts.MaxIterations; i++ {
		req := Request{
			SessionID: s.ID,
			System:    a.systemPrompt(ctx, s),
			Messages:  s.Messages,
			Tools:     toolDefs,
		}

		var (
			resp Response
			err  error
		)
		if canStream {
			resp, err = a.streamOnce(ctx, streamer, req, events)
		} else {
			resp, err = a.provider.Complete(ctx, req)
		}
		if err != nil {
			return Message{}, fmt.Errorf("provider: %w", err)
		}

		s.Append(resp.Message)

		if resp.StopReason == StopEndTurn || resp.StopReason != StopToolUse {
			return resp.Message, nil
		}

		results, err := a.runTools(ctx, resp.Message, s.ID)
		if err != nil {
			return Message{}, err
		}
		if len(results) > 0 {
			s.Append(Message{Role: RoleUser, Content: results})
		}
	}

	return Message{}, ErrMaxIterations
}

// streamOnce invokes the provider's Stream, forwards every event to
// the caller's channel, and returns the terminal Response.
func (a *Agent) streamOnce(ctx context.Context, p StreamingProvider, req Request, out chan<- StreamEvent) (Response, error) {
	inEvents, inReport, err := p.Stream(ctx, req)
	if err != nil {
		return Response{}, err
	}
	for evt := range inEvents {
		select {
		case out <- evt:
		case <-ctx.Done():
			return Response{}, ctx.Err()
		}
	}
	report, ok := <-inReport
	if !ok {
		return Response{}, fmt.Errorf("provider closed report channel without a StreamReport")
	}
	return report.Response, report.Err
}
