package resilience

import (
	"context"
	"testing"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
	"github.com/sebastienrousseau/rousseau-agent/internal/transport"
)

// BenchmarkRecover_Passthrough measures the middleware's steady-state
// cost — success path, no panic. The whole reason to ship Recover is
// that this is cheap; a regression here would be silently expensive.
func BenchmarkRecover_Passthrough(b *testing.B) {
	inner := transport.HandlerFunc(func(context.Context, transport.IncomingMessage) (string, error) {
		return "ok", nil
	})
	wrapped := Recover(inner, "bench", nil)
	msg := transport.IncomingMessage{From: "u1"}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = wrapped.Handle(context.Background(), msg) //nolint:errcheck // bench passthrough
	}
}

// BenchmarkBreaker_Closed measures the breaker overhead on the happy
// path.
func BenchmarkBreaker_Closed(b *testing.B) {
	fp := &noopProvider{}
	br := NewBreakerProvider(fp, BreakerConfig{})
	req := agent.Request{}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = br.Complete(context.Background(), req) //nolint:errcheck // bench passthrough
	}
}

type noopProvider struct{}

func (*noopProvider) Name() string { return "noop" }
func (*noopProvider) Complete(context.Context, agent.Request) (agent.Response, error) {
	return agent.Response{}, nil
}
