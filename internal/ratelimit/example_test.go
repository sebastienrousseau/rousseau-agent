package ratelimit_test

import (
	"context"
	"fmt"

	"github.com/sebastienrousseau/rousseau-agent/internal/ratelimit"
	"github.com/sebastienrousseau/rousseau-agent/internal/transport"
)

// ExampleWrap shows the transport-side wiring pattern: parse the
// operator's config-supplied rate string, build a KeyedLimiter,
// and Wrap the transport.Handler.
func ExampleWrap() {
	rate := ratelimit.MustParseRate("10r/1m") // 10 requests / minute / JID
	limiter := ratelimit.NewKeyedLimiter(rate.Requests, rate.RefillPerSec(), 10000)

	inner := transport.HandlerFunc(func(context.Context, transport.IncomingMessage) (string, error) {
		return "hello", nil
	})
	wrapped := ratelimit.Wrap(inner, limiter, "slack", "")

	for i := 0; i < 12; i++ {
		reply, _ := wrapped.Handle(context.Background(), transport.IncomingMessage{From: "u1"}) //nolint:errcheck // example
		if reply != "hello" {
			fmt.Println(i, reply)
		}
	}
	// Output: 10 You're sending messages too quickly. Try again in a minute.
	// 11 You're sending messages too quickly. Try again in a minute.
}

// ExampleParseRate demonstrates the "Nr/Duration" string shape.
func ExampleParseRate() {
	r, _ := ratelimit.ParseRate("60r/1m") //nolint:errcheck // example
	fmt.Printf("%.1f req/s\n", r.RefillPerSec())
	// Output: 1.0 req/s
}
