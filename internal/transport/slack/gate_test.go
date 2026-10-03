package slack

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDispatchEvent_StrangerFileIsNotDownloaded pins the media gate:
// a file from a user the router would reject is never fetched. Without
// the gate the same fixture is fetched, so the test cannot pass
// vacuously.
func TestDispatchEvent_StrangerFileIsNotDownloaded(t *testing.T) {
	for _, tc := range []struct {
		name      string
		gate      func(string) bool
		wantFetch bool
	}{{"stranger", func(string) bool { return false }, false}, {"no gate", nil, true}} {
		t.Run(tc.name, func(t *testing.T) {
			var fetched atomic.Bool
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasPrefix(r.URL.Path, "/files/") {
					fetched.Store(true)
					_, _ = w.Write([]byte("\x89PNG\r\n\x1a\n0000")) //nolint:errcheck // test writer
					return
				}
				_, _ = w.Write([]byte(`{"ok":true}`)) //nolint:errcheck // test writer
			}))
			defer srv.Close()
			c, err := New(Config{AppToken: "xapp-x", BotToken: "xoxb-y", BaseURL: srv.URL,
				HTTPClient: srv.Client(), IsAllowed: tc.gate}, silentLogger())
			require.NoError(t, err)
			_ = c.dispatchEvent(context.Background(), eventsAPIPayload{Event: slackEvent{ //nolint:errcheck // only the fetch matters
				Type: "message", SubType: "file_share", User: "U-stranger", Channel: "C1", Text: "look",
				Files: []slackFile{{ID: "F1", Mimetype: "image/png", Size: 12, URLPrivateDownload: srv.URL + "/files/F1"}},
			}}, noopHandler())
			assert.Equal(t, tc.wantFetch, fetched.Load())
		})
	}
}
