package matrix

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/transport"
)

// membersServer answers joined_members for any room with members
// joined users, counting the lookups.
func membersServer(t *testing.T, members int, hits *atomic.Int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/joined_members") {
			_, _ = w.Write([]byte(`{}`)) //nolint:errcheck // fixture
			return
		}
		hits.Add(1)
		joined := map[string]any{}
		for i := range members {
			joined["@u"+string(rune('a'+i))+":hs"] = map[string]any{}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"joined": joined}) //nolint:errcheck // fixture
	}))
	t.Cleanup(srv.Close)
	return srv
}

func handleMatrix(t *testing.T, c *Client, room string) transport.IncomingMessage {
	t.Helper()
	var seen transport.IncomingMessage
	called := false
	handler := transport.HandlerFunc(func(_ context.Context, m transport.IncomingMessage) (string, error) {
		seen, called = m, true
		return "", nil
	})
	evt := timelineEvent{Type: "m.room.message", Sender: "@ua:hs", EventID: "$e",
		Content: json.RawMessage(`{"msgtype":"m.text","body":"hi"}`)}
	c.handleEvent(context.Background(), room, evt, handler)
	require.True(t, called)
	return seen
}

func TestHandleEvent_TwoMemberRoomIsDirect(t *testing.T) {
	var hits atomic.Int32
	srv := membersServer(t, 2, &hits)
	c := newTestClient(t, Config{HomeserverURL: srv.URL, HTTPClient: srv.Client()})
	assert.True(t, handleMatrix(t, c, "!dm:hs").IsDirect)
	assert.True(t, handleMatrix(t, c, "!dm:hs").IsDirect)
	assert.Equal(t, int32(1), hits.Load(), "membership is cached per room")
}

func TestHandleEvent_GroupRoomIsNotDirect(t *testing.T) {
	var hits atomic.Int32
	srv := membersServer(t, 3, &hits)
	c := newTestClient(t, Config{HomeserverURL: srv.URL, HTTPClient: srv.Client()})
	assert.False(t, handleMatrix(t, c, "!group:hs").IsDirect)
}

func TestHandleEvent_MembershipErrorIsNotDirect(t *testing.T) {
	c := newTestClient(t, Config{}) // homeserver unreachable
	assert.False(t, handleMatrix(t, c, "!dm:hs").IsDirect)
}
