package discord

import (
	"context"
	"testing"

	"github.com/sebastienrousseau/rousseau-agent/internal/transport"
)

// FuzzHandleFrame drives arbitrary bytes through Discord's Gateway v10
// envelope parser (op / t / d shape). Any panic or unbounded recursion
// indicates a wire-format defect an attacker on the WSS could reach.
func FuzzHandleFrame(f *testing.F) {
	f.Add([]byte(`{"op":10,"d":{"heartbeat_interval":41250}}`))
	f.Add([]byte(`{"op":0,"t":"READY","d":{"user":{"id":"U_BOT"}}}`))
	f.Add([]byte(`{"op":0,"t":"MESSAGE_CREATE","d":{"channel_id":"C1","content":"hi","author":{"id":"U1"}}}`))
	f.Add([]byte(`{"op":0,"t":"MESSAGE_CREATE","d":{"channel_id":"C1","content":"bot","author":{"id":"U2","bot":true}}}`))
	f.Add([]byte(`{"op":11}`))
	f.Add([]byte(`{"op":0,"t":"TYPING_START","d":{"user_id":"U1"}}`))
	f.Add([]byte(``))
	f.Add([]byte(`{`))
	f.Add([]byte(`{"op":0,"t":"MESSAGE_CREATE"}`))

	c, err := New(Config{Token: "t"}, silentLogger())
	if err != nil {
		f.Fatal(err)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("panic on frame %q: %v", data, r)
			}
		}()
		ws := &fakeWS{}
		_ = c.handleFrame(context.Background(), ws, data, //nolint:errcheck // fuzz best-effort
			transport.HandlerFunc(func(context.Context, transport.IncomingMessage) (string, error) {
				return "", nil
			}))
	})
}
