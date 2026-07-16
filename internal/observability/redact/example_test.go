package redact_test

import (
	"bytes"
	"log/slog"

	"github.com/sebastienrousseau/rousseau-agent/internal/observability/redact"
)

// ExampleNew demonstrates the intended wiring in a daemon's newLogger
// factory. Every log record flows through DefaultRules before reaching
// the underlying JSON sink; credentials are replaced with a
// marker so operators can grep logs to see what was scrubbed.
func ExampleNew() {
	var buf bytes.Buffer
	base := slog.NewJSONHandler(&buf, nil)
	scrubber := redact.New(base, redact.DefaultRules())

	logger := slog.New(scrubber)
	logger.Info("provider.call", slog.String("token",
		"sk-ant-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"))

	// Output would contain «redacted:anthropic» in the token field.
	// The full record is JSON so we don't include it here for
	// example stability across Go versions.
}
