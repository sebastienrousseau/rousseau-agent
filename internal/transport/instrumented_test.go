package transport

import (
	"context"
	"errors"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"

	"github.com/sebastienrousseau/rousseau-agent/internal/observability"
)

func TestInstrumentedHandler_countsOkAndError(t *testing.T) {
	observability.TransportIncoming.Reset()
	observability.TransportOutgoing.Reset()

	ok := HandlerFunc(func(context.Context, IncomingMessage) (string, error) {
		return "hi", nil
	})
	bad := HandlerFunc(func(context.Context, IncomingMessage) (string, error) {
		return "", errors.New("boom")
	})

	_, err := InstrumentedHandler("slack", ok).Handle(context.Background(), IncomingMessage{From: "u"})
	assert.NoError(t, err)
	_, err = InstrumentedHandler("slack", bad).Handle(context.Background(), IncomingMessage{From: "u"})
	assert.Error(t, err)
	_, err = InstrumentedHandler("slack", ok).Handle(context.Background(), IncomingMessage{From: "u"})
	assert.NoError(t, err)

	assert.Equal(t, 3.0, testutil.ToFloat64(observability.TransportIncoming.WithLabelValues("slack")))
	assert.Equal(t, 2.0, testutil.ToFloat64(observability.TransportOutgoing.WithLabelValues("slack", "ok")))
	assert.Equal(t, 1.0, testutil.ToFloat64(observability.TransportOutgoing.WithLabelValues("slack", "error")))
}

func TestInstrumentedHandler_labelsByTransportName(t *testing.T) {
	observability.TransportIncoming.Reset()

	handler := HandlerFunc(func(context.Context, IncomingMessage) (string, error) { return "x", nil })
	for _, name := range []string{"slack", "discord", "whatsapp", "email"} {
		_, err := InstrumentedHandler(name, handler).Handle(context.Background(), IncomingMessage{From: "u"})
		assert.NoError(t, err)
	}
	for _, name := range []string{"slack", "discord", "whatsapp", "email"} {
		assert.Equal(t, 1.0, testutil.ToFloat64(observability.TransportIncoming.WithLabelValues(name)),
			"transport %s should have one incoming", name)
	}
}
