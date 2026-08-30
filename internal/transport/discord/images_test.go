package discord

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/media"
	"github.com/sebastienrousseau/rousseau-agent/internal/transport"
)

var pngHeader = []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x00, 0x00, 0x0D}

// cdnServer mimics Discord's signed CDN URL — accepts any GET and
// returns body/status. Unlike Slack the URL is pre-signed so there
// is no auth header to verify; we only assert the pipeline hit us.
type cdnServer struct {
	srv       *httptest.Server
	body      []byte
	status    int
	callCount int
}

func newCDNServer(t *testing.T, body []byte) *cdnServer {
	t.Helper()
	c := &cdnServer{body: body, status: http.StatusOK}
	c.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		c.callCount++
		w.WriteHeader(c.status)
		if _, err := w.Write(c.body); err != nil {
			log.Printf("discord cdnserver: body write: %v", err)
		}
	}))
	t.Cleanup(c.srv.Close)
	return c
}

// messageCreateFrame builds a Gateway MESSAGE_CREATE dispatch with
// one attachment served from cdn.
func messageCreateFrame(t *testing.T, content, contentType string, cdn *cdnServer) []byte {
	t.Helper()
	frame := map[string]any{
		"op": 0,
		"t":  "MESSAGE_CREATE",
		"d": map[string]any{
			"id":         "M1",
			"channel_id": "C0",
			"author":     map[string]any{"id": "U1", "username": "alice"},
			"content":    content,
			"attachments": []map[string]any{{
				"id":           "A1",
				"filename":     "test.png",
				"size":         len(cdn.body),
				"url":          cdn.srv.URL + "/attachments/A1",
				"content_type": contentType,
			}},
		},
	}
	b, err := json.Marshal(frame)
	require.NoError(t, err)
	return b
}

func TestHandleFrame_ImageAttachmentBecomesTransportAttachment(t *testing.T) {
	cdn := newCDNServer(t, pngHeader)
	c, err := New(Config{Token: "bot"}, silentLogger())
	require.NoError(t, err)

	var got transport.IncomingMessage
	handler := transport.HandlerFunc(func(_ context.Context, m transport.IncomingMessage) (string, error) {
		got = m
		return "", nil
	})
	require.NoError(t, c.handleFrame(context.Background(), &fakeWS{},
		messageCreateFrame(t, "what is this?", "image/png", cdn), handler))

	assert.Equal(t, "what is this?", got.Body)
	require.Len(t, got.Attachments, 1)
	assert.Equal(t, "image/png", got.Attachments[0].MediaType)
	assert.Equal(t, pngHeader, got.Attachments[0].Data)
	assert.Equal(t, 1, cdn.callCount)
}

func TestHandleFrame_ImageOnlyStillRoutes(t *testing.T) {
	cdn := newCDNServer(t, pngHeader)
	c, err := New(Config{Token: "bot"}, silentLogger())
	require.NoError(t, err)

	var got transport.IncomingMessage
	handler := transport.HandlerFunc(func(_ context.Context, m transport.IncomingMessage) (string, error) {
		got = m
		return "", nil
	})
	require.NoError(t, c.handleFrame(context.Background(), &fakeWS{},
		messageCreateFrame(t, "", "image/png", cdn), handler))
	assert.Empty(t, got.Body)
	require.Len(t, got.Attachments, 1)
}

func TestHandleFrame_NonImageAttachmentNotDownloaded(t *testing.T) {
	cdn := newCDNServer(t, []byte("pdfbytes"))
	c, err := New(Config{Token: "bot"}, silentLogger())
	require.NoError(t, err)

	var got transport.IncomingMessage
	handler := transport.HandlerFunc(func(_ context.Context, m transport.IncomingMessage) (string, error) {
		got = m
		return "", nil
	})
	require.NoError(t, c.handleFrame(context.Background(), &fakeWS{},
		messageCreateFrame(t, "here", "application/pdf", cdn), handler))
	assert.Equal(t, 0, cdn.callCount, "non-image envelope MIME must skip the download")
	assert.Empty(t, got.Attachments)
	assert.Equal(t, "here", got.Body)
}

func TestHandleFrame_ImageDownloadFailureDropsAttachmentKeepsText(t *testing.T) {
	cdn := newCDNServer(t, nil)
	cdn.status = http.StatusInternalServerError
	c, err := New(Config{Token: "bot"}, silentLogger())
	require.NoError(t, err)

	var got transport.IncomingMessage
	handler := transport.HandlerFunc(func(_ context.Context, m transport.IncomingMessage) (string, error) {
		got = m
		return "", nil
	})
	require.NoError(t, c.handleFrame(context.Background(), &fakeWS{},
		messageCreateFrame(t, "with pic", "image/png", cdn), handler))
	assert.Empty(t, got.Attachments)
	assert.Equal(t, "with pic", got.Body)
}

func TestHandleFrame_OversizeImageIsDropped(t *testing.T) {
	cdn := newCDNServer(t, pngHeader)
	c, err := New(Config{
		Token:       "bot",
		MediaPolicy: media.Policy{MaxImageBytes: 3},
	}, silentLogger())
	require.NoError(t, err)

	var got transport.IncomingMessage
	handler := transport.HandlerFunc(func(_ context.Context, m transport.IncomingMessage) (string, error) {
		got = m
		return "", nil
	})
	require.NoError(t, c.handleFrame(context.Background(), &fakeWS{},
		messageCreateFrame(t, "cap", "image/png", cdn), handler))
	assert.Empty(t, got.Attachments)
}

func TestHandleFrame_EnvelopeMIMELyingLogsAndDelivers(t *testing.T) {
	cdn := newCDNServer(t, pngHeader)
	c, err := New(Config{Token: "bot"}, silentLogger())
	require.NoError(t, err)

	var got transport.IncomingMessage
	handler := transport.HandlerFunc(func(_ context.Context, m transport.IncomingMessage) (string, error) {
		got = m
		return "", nil
	})
	require.NoError(t, c.handleFrame(context.Background(), &fakeWS{},
		messageCreateFrame(t, "", "image/jpeg", cdn), handler))
	require.Len(t, got.Attachments, 1)
	assert.Equal(t, "image/png", got.Attachments[0].MediaType,
		"sniffed MIME wins over the envelope's claim")
}

func TestHandleFrame_AttachmentWithoutURLIsSkipped(t *testing.T) {
	frame := []byte(`{"op":0,"t":"MESSAGE_CREATE","d":{"id":"M1","channel_id":"C0","author":{"id":"U1"},"content":"hi","attachments":[{"id":"A1","content_type":"image/png"}]}}`)
	c, err := New(Config{Token: "bot"}, silentLogger())
	require.NoError(t, err)

	var got transport.IncomingMessage
	handler := transport.HandlerFunc(func(_ context.Context, m transport.IncomingMessage) (string, error) {
		got = m
		return "", nil
	})
	require.NoError(t, c.handleFrame(context.Background(), &fakeWS{}, frame, handler))
	assert.Empty(t, got.Attachments)
	assert.Equal(t, "hi", got.Body)
}

func TestIsImageContentType(t *testing.T) {
	assert.True(t, isImageContentType("image/png"))
	assert.True(t, isImageContentType("image/webp"))
	assert.False(t, isImageContentType("application/pdf"))
	assert.False(t, isImageContentType(""))
	assert.False(t, isImageContentType("image"))
}
