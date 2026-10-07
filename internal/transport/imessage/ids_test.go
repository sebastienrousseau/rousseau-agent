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

func TestPollOnce_CarriesChatAndMessageGUID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/message" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[{"guid":"m1","text":"hi","isFromMe":false,"dateCreated":1700000000000,"handle":{"address":"+15551112222"},"chats":[{"guid":"chat1"}]}]}`)) //nolint:errcheck // test writer
			return
		}
		_, _ = w.Write([]byte(`{}`)) //nolint:errcheck // test writer
	}))
	defer srv.Close()
	c := iMessageClient(t, srv, Config{})

	var got transport.IncomingMessage
	handler := transport.HandlerFunc(func(_ context.Context, m transport.IncomingMessage) (string, error) {
		got = m
		return "", nil
	})
	require.NoError(t, c.pollOnce(context.Background(), handler))
	assert.Equal(t, "chat1", got.Conversation)
	assert.Equal(t, "m1", got.MessageID)
}
