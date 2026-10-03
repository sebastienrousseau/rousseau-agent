package imessage

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/transport"
)

// TestHandleMessage_StrangerVoiceIsNotTranscribed: see the telegram twin.
func TestHandleMessage_StrangerVoiceIsNotTranscribed(t *testing.T) {
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
			ft := &fakeTranscriber{reply: "hi"}
			c, err := New(Config{BaseURL: srv.URL, Password: "p", Transcriber: ft,
				HTTPClient: srv.Client(), IsAllowed: tc.gate}, silentLogger())
			require.NoError(t, err)
			c.handleMessage(context.Background(), messageRecord{
				GUID: "g", Handle: handleRecord{Address: "+15550000"}, Chats: []chatRecord{{GUID: "c"}},
				Attachments: []attachmentRecord{{GUID: "a", MimeType: "audio/x-caf"}},
			}, transport.HandlerFunc(func(context.Context, transport.IncomingMessage) (string, error) { return "", nil }))
			assert.Equal(t, tc.called, ft.audio != nil)
		})
	}
}
