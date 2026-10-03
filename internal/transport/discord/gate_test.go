package discord

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
			ft := &fakeTranscriber{reply: "hello"}
			c, err := New(Config{Token: "t", BaseURL: srv.URL, HTTPClient: srv.Client(),
				Transcriber: ft, IsAllowed: tc.gate}, silentLogger())
			require.NoError(t, err)
			_ = c.handleMessage(context.Background(), discordMessage{ //nolint:errcheck // only the transcriber matters
				ChannelID: "C", Author: discordUser{ID: "U-stranger"},
				Attachments: []discordAttachment{{URL: srv.URL + "/att.ogg", ContentType: "audio/ogg"}},
			}, noopHandler())
			assert.Equal(t, tc.called, ft.audio != nil)
		})
	}
}
