package slack

import (
	"context"
	"testing"

	"github.com/sebastienrousseau/rousseau-agent/internal/transport"
)

// FuzzHandleFrame drives arbitrary bytes through Slack's Socket Mode
// envelope parser. Any crash — panic, out-of-bounds, or unbounded
// recursion — signals a wire-format defect the model could reach via
// a malicious Socket Mode server (or a man-in-the-middle on the WSS).
//
// The seed corpus covers every event type the parser explicitly
// dispatches on: hello, disconnect, events_api envelope, an
// unhandled event, and a malformed body. The fuzzer then mutates
// from there.
func FuzzHandleFrame(f *testing.F) {
	f.Add([]byte(`{"type":"hello","num_connections":1}`))
	f.Add([]byte(`{"type":"disconnect","reason":"warning"}`))
	f.Add([]byte(`{"envelope_id":"env-1","type":"events_api","payload":{"event":{"type":"message","channel":"C1","user":"U1","text":"hi","subtype":""}}}`))
	f.Add([]byte(`{"envelope_id":"env-2","type":"events_api","payload":{"event":{"type":"reaction_added","user":"U1"}}}`))
	f.Add([]byte(`{"type":"unknown"}`))
	f.Add([]byte(`{"type":"events_api","payload":{"event":null}}`))
	f.Add([]byte(``))
	f.Add([]byte(`{`))
	f.Add([]byte(`{"envelope_id":"","type":"events_api","payload":{"event":{"type":"message","channel":"","user":"","text":""}}}`))

	c, err := New(Config{AppToken: "xapp-x", BotToken: "xoxb-y"}, silentLogger())
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
