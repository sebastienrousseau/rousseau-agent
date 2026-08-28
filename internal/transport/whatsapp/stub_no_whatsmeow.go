//go:build no_whatsmeow

// Package whatsapp — this stub provides the public surface (Client, New, Start, Deliver,
// Stop) that cli/whatsapp.go depends on when the whatsmeow transport
// has been compiled out — used by the :lite container tag. Every
// entry point returns an error at construction so operators see
// exactly why the transport is unavailable.
package whatsapp

import (
	"context"
	"errors"
	"log/slog"

	"github.com/sebastienrousseau/rousseau-agent/internal/transport"
)

// errCompiledOut is returned by every stub method. Message is stable
// so integration tests + operator log-grep can pin on it.
var errCompiledOut = errors.New("whatsapp: transport compiled out under -tags=no_whatsmeow (use the default :latest or :distroless container)")

// Client is the stub whatsmeow-less client. Every method returns
// errCompiledOut — the whole point of the lite build is to be
// smaller, not to secretly still work.
type Client struct{}

// New constructs a stub Client that immediately fails Start. Never
// returns a nil Client so calling code can safely defer .Stop().
func New(_ Config, _ *slog.Logger) (*Client, error) {
	return &Client{}, nil
}

// Start satisfies transport.Transport. Always returns errCompiledOut.
func (*Client) Start(_ context.Context, _ transport.Handler) error {
	return errCompiledOut
}

// Deliver satisfies transport.Transport. Always returns errCompiledOut.
func (*Client) Deliver(_ context.Context, _, _ string) error {
	return errCompiledOut
}

// Stop satisfies transport.Transport. Nothing to stop; returns nil.
func (*Client) Stop() error { return nil }

// Name satisfies transport.Transport.
func (*Client) Name() string { return "whatsapp" }
