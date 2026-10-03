package signal

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/transport"
)

// TestHandleReceive_StrangerVoiceIsNotTranscribed: see the telegram twin.
func TestHandleReceive_StrangerVoiceIsNotTranscribed(t *testing.T) {
	for _, tc := range []struct {
		name   string
		gate   func(string) bool
		called bool
	}{{"stranger", func(string) bool { return false }, false}, {"no gate", nil, true}} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(dir, "att-1"), []byte("AUDIO"), 0o600))
			ft := &fakeTranscriber{reply: "hello"}
			c, err := New(Config{Account: "+1", Transcriber: ft, AttachmentsDir: dir, IsAllowed: tc.gate}, silentLogger())
			require.NoError(t, err)
			var params receiveParams
			require.NoError(t, json.Unmarshal([]byte(`{"envelope":{"sourceNumber":"+15550000","timestamp":1,`+
				`"dataMessage":{"attachments":[{"id":"att-1","contentType":"audio/aac"}]}}}`), &params))
			_ = c.handleReceive(context.Background(), params, //nolint:errcheck // only the transcriber matters
				transport.HandlerFunc(func(context.Context, transport.IncomingMessage) (string, error) { return "", nil }))
			assert.Equal(t, tc.called, ft.audio != nil)
		})
	}
}
