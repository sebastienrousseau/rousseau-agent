package telegram

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRoute_StrangerVoiceIsNotTranscribed pins the media gate: a voice
// note from a sender the router would reject is neither downloaded nor
// transcribed. The same fixture without the gate is transcribed, so
// the test cannot pass vacuously.
func TestRoute_StrangerVoiceIsNotTranscribed(t *testing.T) {
	for _, tc := range []struct {
		name      string
		gate      func(string) bool
		wantCalls int
	}{
		{"stranger", func(string) bool { return false }, 0},
		{"no gate", nil, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := telegramTestServer(t, "voice/foo.oga", []byte("opus"))
			defer srv.Close()
			ft := &fakeTranscriber{reply: "hello"}
			c, err := New(Config{Token: "t", BaseURL: srv.URL, Transcriber: ft, IsAllowed: tc.gate}, silentLogger())
			require.NoError(t, err)
			c.route(context.Background(), telegramUpdate{Message: &telegramMessage{
				Chat: telegramChat{ID: 42}, Date: time.Now().Unix(),
				Voice: &telegramVoice{FileID: "v", MimeType: "audio/ogg"},
			}}, &captureHandler{})
			assert.Equal(t, tc.wantCalls, ft.called)
		})
	}
}
