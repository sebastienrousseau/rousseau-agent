package slack

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDefaultDial_Adapter drives the real websocket dialer against
// an in-process echo server so the WSConn adapter methods (Read/
// Write/Close) are covered.
func TestDefaultDial_Adapter(t *testing.T) {
	done := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		_, msg, err := c.Read(r.Context())
		if err != nil {
			return
		}
		_ = c.Write(r.Context(), websocket.MessageText, msg) //nolint:errcheck // fixture
		<-done
		_ = c.CloseNow() //nolint:errcheck // best-effort cleanup
	}))
	defer func() { close(done); srv.Close() }()

	url := "ws" + srv.URL[len("http"):]
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, err := defaultDial(ctx, url)
	require.NoError(t, err)
	require.NoError(t, conn.Write(ctx, []byte("hi")))
	got, err := conn.Read(ctx)
	require.NoError(t, err)
	assert.Equal(t, "hi", string(got))
	// Close error is best-effort — the WS half-close race isn't a
	// bug in the adapter, it's Go/websocket semantics. Just call it.
	_ = conn.Close(websocket.StatusNormalClosure, "test done") //nolint:errcheck // half-close race
}

func TestDefaultDial_BadURLErrors(t *testing.T) {
	_, err := defaultDial(context.Background(), "ws://127.0.0.1:1/nonexistent")
	assert.Error(t, err)
}
