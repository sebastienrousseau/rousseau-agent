package imessage

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/media"
	"github.com/sebastienrousseau/rousseau-agent/internal/transport"
)

var pngHeader = []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x00, 0x00, 0x0D}

// attachmentServer stands in for BlueBubbles's attachment download
// endpoint. Any GET under /api/v1/attachment/ with the right
// password returns body/status.
func attachmentServer(t *testing.T, password string, body []byte, status int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/v1/attachment/") ||
			!strings.HasSuffix(r.URL.Path, "/download") {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("password") != password {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.WriteHeader(status)
		_, _ = w.Write(body) //nolint:errcheck // test writer
	}))
}

func iMessageClient(t *testing.T, srv *httptest.Server, cfg Config) *Client {
	t.Helper()
	if cfg.BaseURL == "" {
		cfg.BaseURL = srv.URL
	}
	if cfg.Password == "" {
		cfg.Password = "p"
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = srv.Client()
	}
	c, err := New(cfg, silentLogger())
	require.NoError(t, err)
	return c
}

func TestCollectImageAttachments_HappyPath(t *testing.T) {
	srv := attachmentServer(t, "p", pngHeader, http.StatusOK)
	defer srv.Close()

	c := iMessageClient(t, srv, Config{})
	got := c.collectImageAttachments(context.Background(), []attachmentRecord{{
		GUID: "abc", MimeType: "image/png",
	}})
	require.Len(t, got, 1)
	assert.Equal(t, "image/png", got[0].MediaType)
	assert.Equal(t, pngHeader, got[0].Data)
}

func TestCollectImageAttachments_NonImageSkipped(t *testing.T) {
	// The server must not be hit for non-image types.
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Fatal("no download should happen for non-image attachments")
	}))
	defer srv.Close()

	c := iMessageClient(t, srv, Config{})
	got := c.collectImageAttachments(context.Background(), []attachmentRecord{{
		GUID: "vid", MimeType: "video/mp4",
	}})
	assert.Empty(t, got)
}

func TestCollectImageAttachments_EmptyGUIDSkipped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Fatal("no download should happen when GUID is empty")
	}))
	defer srv.Close()

	c := iMessageClient(t, srv, Config{})
	got := c.collectImageAttachments(context.Background(), []attachmentRecord{{
		GUID: "", MimeType: "image/png",
	}})
	assert.Empty(t, got)
}

func TestCollectImageAttachments_DownloadFailureDropped(t *testing.T) {
	srv := attachmentServer(t, "p", nil, http.StatusInternalServerError)
	defer srv.Close()

	c := iMessageClient(t, srv, Config{})
	got := c.collectImageAttachments(context.Background(), []attachmentRecord{{
		GUID: "abc", MimeType: "image/png",
	}})
	assert.Empty(t, got)
}

func TestCollectImageAttachments_OversizeDropped(t *testing.T) {
	srv := attachmentServer(t, "p", pngHeader, http.StatusOK)
	defer srv.Close()

	c := iMessageClient(t, srv, Config{
		MediaPolicy: media.Policy{MaxImageBytes: 3},
	})
	got := c.collectImageAttachments(context.Background(), []attachmentRecord{{
		GUID: "abc", MimeType: "image/png",
	}})
	assert.Empty(t, got)
}

func TestCollectImageAttachments_EnvelopeMIMELyingLogsAndDelivers(t *testing.T) {
	srv := attachmentServer(t, "p", pngHeader, http.StatusOK)
	defer srv.Close()

	c := iMessageClient(t, srv, Config{})
	got := c.collectImageAttachments(context.Background(), []attachmentRecord{{
		GUID: "abc", MimeType: "image/jpeg", // envelope lies
	}})
	require.Len(t, got, 1)
	assert.Equal(t, "image/png", got[0].MediaType)
}

func TestCollectImageAttachments_MultipleFilesFolded(t *testing.T) {
	srv := attachmentServer(t, "p", pngHeader, http.StatusOK)
	defer srv.Close()

	c := iMessageClient(t, srv, Config{})
	got := c.collectImageAttachments(context.Background(), []attachmentRecord{
		{GUID: "a", MimeType: "image/png"},
		{GUID: "b", MimeType: "image/png"},
	})
	require.Len(t, got, 2)
}

func TestCollectImageAttachments_EmptySliceReturnsNil(t *testing.T) {
	c, err := New(Config{BaseURL: "http://x", Password: "p"}, silentLogger())
	require.NoError(t, err)
	assert.Nil(t, c.collectImageAttachments(context.Background(), nil))
}

// pollOnce is the smallest exercise of the full poll path with an
// image; asserts the message reaches the handler with Attachments
// populated. Uses a paginated response with one image-bearing message
// and no text.
func TestPollOnce_ImageAttachmentReachesHandler(t *testing.T) {
	// Serve both the /message list and the /attachment download.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/v1/attachment/") {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(pngHeader) //nolint:errcheck // test writer
			return
		}
		if r.URL.Path == "/api/v1/message" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[{"guid":"m1","text":"","isFromMe":false,"dateCreated":1700000000000,"handle":{"address":"+15551112222"},"chats":[{"guid":"chat1"}],"attachments":[{"guid":"a1","mimeType":"image/png"}]}]}`)) //nolint:errcheck // test writer
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	c := iMessageClient(t, srv, Config{})

	var got transport.IncomingMessage
	handler := transport.HandlerFunc(func(_ context.Context, m transport.IncomingMessage) (string, error) {
		got = m
		return "", nil
	})
	require.NoError(t, c.pollOnce(context.Background(), handler))
	assert.Equal(t, "+15551112222", got.From)
	assert.Empty(t, got.Body)
	require.Len(t, got.Attachments, 1)
	assert.Equal(t, "image/png", got.Attachments[0].MediaType)
}
