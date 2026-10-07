package agent

import (
	"context"
	"fmt"

	"github.com/sebastienrousseau/rousseau-agent/internal/progress"
)

// TurnStream is the streaming twin of Turn. It is the same loop —
// same compression pass, control gates, cache hints, cost recording,
// tool phase, iteration budget and reliability samples — with each
// provider round-trip streamed through the optional StreamingProvider
// interface (falling back to Complete when the provider does not
// implement it). Before the loops were unified, the streaming path
// silently skipped cost telemetry and provider metrics.
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
	streamer, canStream := a.provider.(StreamingProvider)
	if !canStream {
		return a.run(ctx, s, completer{op: "complete", fn: a.completeOnce})
	}
	return a.run(ctx, s, completer{op: "stream", fn: func(ctx context.Context, req Request, iteration int) (Response, error) {
		return a.roundTrip(ctx, "provider.stream", req, iteration, func(ctx context.Context) (Response, error) {
			return a.streamOnce(ctx, streamer, req, events, iteration)
		})
	}})
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
