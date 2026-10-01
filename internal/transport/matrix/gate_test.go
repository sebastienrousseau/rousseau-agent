package matrix

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/transport"
)

// TestHandleEvent_StrangerVoiceIsNotTranscribed: see the telegram twin.
func TestHandleEvent_StrangerVoiceIsNotTranscribed(t *testing.T) {
	for _, tc := range []struct {
		name   string
		gate   func(string) bool
		called bool
	}{{"stranger", func(string) bool { return false }, false}, {"no gate", nil, true}} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte("VOICE")) //nolint:errcheck // test writer
			}))
			defer srv.Close()
			ft := &fakeTranscriber{reply: "hello"}
			c, err := New(Config{HomeserverURL: srv.URL, AccessToken: "tok", Transcriber: ft,
				HTTPClient: srv.Client(), IsAllowed: tc.gate}, silentLogger())
			require.NoError(t, err)
			c.handleEvent(context.Background(), "!r:x", timelineEvent{
				Type: "m.room.message", Sender: "@stranger:x",
				Content: mustJSON(t, map[string]any{"msgtype": "m.audio", "url": "mxc://matrix.org/abc",
					"info": map[string]string{"mimetype": "audio/ogg"}}),
			}, transport.HandlerFunc(func(context.Context, transport.IncomingMessage) (string, error) { return "", nil }))
			assert.Equal(t, tc.called, ft.audio != nil)
		})
	}
}
