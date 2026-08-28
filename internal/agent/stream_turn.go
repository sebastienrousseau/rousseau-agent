package agent

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/sebastienrousseau/rousseau-agent/internal/progress"
	"github.com/sebastienrousseau/rousseau-agent/internal/toolcontext"
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

	start := time.Now()
	a.emit(ctx, s, progress.Event{Kind: progress.KindTurnStarted})
	msg, err := a.turnStream(ctx, s, events)
	a.emitTerminal(ctx, s, start, err)
	return msg, err
}

// turnStream is TurnStream's body, split out so the exported method
// can bracket every exit path with the progress terminal event.
func (a *Agent) turnStream(ctx context.Context, s *Session, events chan<- StreamEvent) (Message, error) {
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
		if err := gate(ctx); err != nil {
			return Message{}, err
		}
		for _, m := range drainSteered(ctx) {
			s.Append(m)
		}

		req := Request{
			SessionID: s.ID,
			System:    a.systemPrompt(ctx, s),
			Messages:  s.Messages,
			Tools:     toolDefs,
		}

		a.emit(ctx, s, progress.Event{Kind: progress.KindThinking, Iteration: i + 1})
		var (
			resp Response
			err  error
		)
		if canStream {
			resp, err = a.streamOnce(ctx, streamer, req, events, i+1)
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

		// Match the non-streaming Turn: inject per-turn state for tools
		// that need it (e.g. spawn_subagent).
		toolCtx := toolcontext.WithLogger(
			toolcontext.WithProvider(
				toolcontext.WithSession(ctx, s),
				a.provider),
			a.logger)

		results, err := a.runTools(toolCtx, resp.Message, s.ID)
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
// the caller's channel, lifts each one into the progress model, and
// returns the terminal Response.
//
// Lifting rather than duplicating is the point: StreamEvent already
// describes a single provider round-trip, so the progress layer
// translates it instead of asking providers to emit a second, parallel
// event stream.
func (a *Agent) streamOnce(ctx context.Context, p StreamingProvider, req Request, out chan<- StreamEvent, iteration int) (Response, error) {
	inEvents, inReport, err := p.Stream(ctx, req)
	if err != nil {
		return Response{}, err
	}
	for evt := range inEvents {
		if ev, ok := liftStreamEvent(evt, iteration); ok {
			a.emitEvent(ctx, ev)
		}
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

// liftStreamEvent maps a provider StreamEvent onto the progress model.
// Kinds with no progress meaning (start, result, other) report false.
func liftStreamEvent(evt StreamEvent, iteration int) (progress.Event, bool) {
	switch evt.Kind {
	case StreamTextDelta:
		if evt.Delta == "" {
			return progress.Event{}, false
		}
		return progress.Event{Kind: progress.KindLLMDelta, Text: evt.Delta, Iteration: iteration}, true
	case StreamToolUse:
		return progress.Event{Kind: progress.KindToolStarted, Tool: "tool", Iteration: iteration}, true
	default:
		return progress.Event{}, false
	}
}
