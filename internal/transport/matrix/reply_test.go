package matrix

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/transport"
)

func TestRoute_IDsAndChunkedReply(t *testing.T) {
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			raw, _ := io.ReadAll(r.Body) //nolint:errcheck // test fixture
			var p struct{ Body string }
			_ = json.Unmarshal(raw, &p) //nolint:errcheck // test fixture
			bodies = append(bodies, p.Body)
		}
		_, _ = w.Write([]byte(`{}`)) //nolint:errcheck // test fixture
	}))
	defer srv.Close()
	c, err := New(Config{HomeserverURL: srv.URL, AccessToken: "t", UserID: "@bot:matrix.org", HTTPClient: srv.Client()}, silentLogger())
	require.NoError(t, err)

	long := strings.Repeat("paragraph of text\n\n", 3000) // ~57 KiB
	var seen transport.IncomingMessage
	handler := transport.HandlerFunc(func(_ context.Context, m transport.IncomingMessage) (string, error) {
		seen = m
		return long, nil
	})
	resp := &syncResponse{Rooms: roomsData{Join: map[string]joinedRoom{
		"!abc:matrix.org": {Timeline: timeline{Events: []timelineEvent{{
			Type: "m.room.message", Sender: "@user:matrix.org", EventID: "$ev1",
			OriginServerTS: 1_700_000_000_000,
			Content:        json.RawMessage(`{"msgtype":"m.text","body":"hello"}`),
		}}}},
	}}}
	c.route(context.Background(), resp, handler)

	assert.Equal(t, "!abc:matrix.org", seen.Conversation)
	assert.Equal(t, "$ev1", seen.MessageID)
	require.Len(t, bodies, 2)
	for _, b := range bodies {
		assert.LessOrEqual(t, len(b), maxTextLen)
	}
}
