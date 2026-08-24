package hooks_test

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent/hooks"
)

// BenchmarkRun_NoHooks measures the fast path — no hooks configured
// for the event, which is the common case for daemons that don't
// enable any hook scripts. The agent loop calls hooks.Run on every
// pre_tool_use, so this must be effectively free.
func BenchmarkRun_NoHooks(b *testing.B) {
	s := hooks.New(nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	payload := []byte(`{"event":"pre_tool_use","tool_name":"bash"}`)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = s.Run(ctx, hooks.EventPreToolUse, payload)
	}
}

// BenchmarkMarshalPreToolUse benchmarks the payload serialisation
// on the pre_tool_use path — runs once per tool call when at least
// one hook is configured.
func BenchmarkMarshalPreToolUse(b *testing.B) {
	input := []byte(`{"command":"ls -la /workspace/some/deep/path"}`)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = hooks.MarshalPreToolUse("session-id-123", "bash", input)
	}
}
