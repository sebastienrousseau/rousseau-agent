package resilience

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/sebastienrousseau/rousseau-agent/internal/transport"
)

func silentLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestRecover_CatchesPanic(t *testing.T) {
	inner := transport.HandlerFunc(func(context.Context, transport.IncomingMessage) (string, error) {
		panic("boom")
	})
	wrapped := Recover(inner, "test", silentLogger())
	reply, err := wrapped.Handle(context.Background(), transport.IncomingMessage{From: "u"})
	assert.Empty(t, reply)
	assert.ErrorContains(t, err, "recovered from panic")
	assert.ErrorContains(t, err, "test")
}

func TestRecover_PassthroughOnSuccess(t *testing.T) {
	inner := transport.HandlerFunc(func(context.Context, transport.IncomingMessage) (string, error) {
		return "pong", nil
	})
	wrapped := Recover(inner, "test", silentLogger())
	reply, err := wrapped.Handle(context.Background(), transport.IncomingMessage{})
	assert.NoError(t, err)
	assert.Equal(t, "pong", reply)
}

func TestRecover_PassthroughOnError(t *testing.T) {
	sentinel := errors.New("upstream failure")
	inner := transport.HandlerFunc(func(context.Context, transport.IncomingMessage) (string, error) {
		return "", sentinel
	})
	wrapped := Recover(inner, "test", silentLogger())
	_, err := wrapped.Handle(context.Background(), transport.IncomingMessage{})
	assert.ErrorIs(t, err, sentinel)
}

func TestRecover_NilLoggerFallsBack(t *testing.T) {
	inner := transport.HandlerFunc(func(context.Context, transport.IncomingMessage) (string, error) {
		panic("boom")
	})
	wrapped := Recover(inner, "test", nil)
	_, err := wrapped.Handle(context.Background(), transport.IncomingMessage{})
	assert.Error(t, err)
}

func TestRecoverFunc_CatchesPanic(t *testing.T) {
	wrapped := RecoverFunc("t", silentLogger(),
		func(context.Context, transport.IncomingMessage) (string, error) {
			panic("boom")
		})
	_, err := wrapped.Handle(context.Background(), transport.IncomingMessage{})
	assert.Error(t, err)
}

// TestRecover_PanicPropertyNeverCrashes runs a family of pathological
// panic values (nil, error, struct, int, string, etc.) through the
// middleware to verify none crash the process.
func TestRecover_PanicPropertyNeverCrashes(t *testing.T) {
	values := []any{
		"string",
		42,
		errors.New("errval"),
		struct{ X int }{X: 1},
		nil, // panic(nil) is legal
	}
	for _, v := range values {
		inner := transport.HandlerFunc(func(context.Context, transport.IncomingMessage) (string, error) {
			panic(v)
		})
		wrapped := Recover(inner, "prop", silentLogger())
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		_, err := wrapped.Handle(ctx, transport.IncomingMessage{})
		cancel()
		// nil panics recover as PanicNilError (Go 1.21+), still counted.
		assert.Error(t, err, "value=%v should surface an error", v)
	}
}
