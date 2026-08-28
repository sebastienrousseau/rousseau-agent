package resilience_test

import (
	"context"
	"fmt"

	"github.com/sebastienrousseau/rousseau-agent/internal/resilience"
	"github.com/sebastienrousseau/rousseau-agent/internal/transport"
)

// ExampleRecover shows the transport-side wrapping pattern. Panics
// in the inner Handle are caught, logged (in production; discarded
// here for output stability), and translated to an error.
func ExampleRecover() {
	inner := transport.HandlerFunc(func(context.Context, transport.IncomingMessage) (string, error) {
		panic("simulated crash")
	})
	safe := resilience.Recover(inner, "slack", nil)
	_, err := safe.Handle(context.Background(), transport.IncomingMessage{From: "u1"})
	if err != nil {
		fmt.Println("caught:", err != nil)
	}
	// Output: caught: true
}

// ExampleNonRetryable shows how to mark an error so the breaker
// treats it as "the caller's fault, not the upstream's" and does
// not count it as a failure.
func ExampleNonRetryable() {
	authErr := fmt.Errorf("HTTP 401 Unauthorized")
	wrapped := resilience.NonRetryable(authErr)
	fmt.Println(wrapped.Error())
	// Output: HTTP 401 Unauthorized
}
