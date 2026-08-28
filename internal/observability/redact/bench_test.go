package redact

import (
	"context"
	"io"
	"log/slog"
	"testing"
)

// BenchmarkHandler_Log measures the steady-state cost of a single
// log record passing through the redact middleware. Default rule
// set (9 rules) is applied to every attribute.
func BenchmarkHandler_Log(b *testing.B) {
	inner := slog.NewJSONHandler(io.Discard, nil)
	h := New(inner, DefaultRules())
	logger := slog.New(h)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		logger.Info("provider.call",
			slog.String("provider", "anthropic"),
			slog.String("body", `Authorization: Bearer sk-ant-`+longToken()),
			slog.Int("tokens_in", 42),
		)
	}
}

// BenchmarkHandler_CleanRecord measures a record with no secrets —
// the common case; every rule fails to match and the record passes
// through unchanged.
func BenchmarkHandler_CleanRecord(b *testing.B) {
	inner := slog.NewJSONHandler(io.Discard, nil)
	h := New(inner, DefaultRules())
	logger := slog.New(h)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		logger.Info("handler.ok", slog.Int("count", 42), slog.String("user", "alice"))
	}
}

func longToken() string {
	return "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
}

// Ensure ctx-aware Enabled path is benchmarked too.
func BenchmarkHandler_Enabled(b *testing.B) {
	inner := slog.NewJSONHandler(io.Discard, nil)
	h := New(inner, DefaultRules())
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = h.Enabled(ctx, slog.LevelInfo)
	}
}
