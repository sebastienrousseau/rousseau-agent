package matrix

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/media"
)

var pngHeader = []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x00, 0x00, 0x0D}

// mediaServer stands in for the homeserver's authenticated media
// download endpoint. Any GET under /media/download/ returns body.
func mediaServer(t *testing.T, body []byte, status int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/media/download/") {
			http.NotFound(w, r)
			return
		}
		if status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		_, _ = w.Write(body) //nolint:errcheck // test writer
	}))
}

func TestCollectImageAttachments_HappyPath(t *testing.T) {
	srv := mediaServer(t, pngHeader, http.StatusOK)
	defer srv.Close()

	c, err := New(Config{
		HomeserverURL: srv.URL, AccessToken: "tok", HTTPClient: srv.Client(),
	}, silentLogger())
	require.NoError(t, err)

	got := c.collectImageAttachments(context.Background(), mustJSON(t, map[string]any{
		"msgtype": "m.image",
		"url":     "mxc://matrix.org/pic123",
		"info":    map[string]string{"mimetype": "image/png"},
	}))
	require.Len(t, got, 1)
	assert.Equal(t, "image/png", got[0].MediaType)
	assert.Equal(t, pngHeader, got[0].Data)
}

func TestCollectImageAttachments_NonImageMsgTypeSkipped(t *testing.T) {
	c, err := New(Config{
		HomeserverURL: "http://unused", AccessToken: "tok",
	}, silentLogger())
	require.NoError(t, err)
	got := c.collectImageAttachments(context.Background(), mustJSON(t, map[string]any{
		"msgtype": "m.audio",
		"url":     "mxc://matrix.org/aud123",
	}))
	assert.Nil(t, got)
}

func TestCollectImageAttachments_MissingURLSkipped(t *testing.T) {
	c, err := New(Config{HomeserverURL: "http://unused", AccessToken: "tok"}, silentLogger())
	require.NoError(t, err)
	got := c.collectImageAttachments(context.Background(), mustJSON(t, map[string]any{
		"msgtype": "m.image",
	}))
	assert.Nil(t, got)
}

func TestCollectImageAttachments_MalformedMXCSkipped(t *testing.T) {
	c, err := New(Config{HomeserverURL: "http://unused", AccessToken: "tok"}, silentLogger())
	require.NoError(t, err)
	got := c.collectImageAttachments(context.Background(), mustJSON(t, map[string]any{
		"msgtype": "m.image",
		"url":     "http://not-mxc/pic",
	}))
	assert.Nil(t, got)
}

func TestCollectImageAttachments_HTTPFailureDropped(t *testing.T) {
	srv := mediaServer(t, nil, http.StatusInternalServerError)
	defer srv.Close()

	c, err := New(Config{
		HomeserverURL: srv.URL, AccessToken: "tok", HTTPClient: srv.Client(),
	}, silentLogger())
	require.NoError(t, err)

	got := c.collectImageAttachments(context.Background(), mustJSON(t, map[string]any{
		"msgtype": "m.image",
		"url":     "mxc://matrix.org/pic123",
	}))
	assert.Nil(t, got)
}

func TestCollectImageAttachments_OversizeDropped(t *testing.T) {
	srv := mediaServer(t, pngHeader, http.StatusOK)
	defer srv.Close()

	c, err := New(Config{
		HomeserverURL: srv.URL, AccessToken: "tok", HTTPClient: srv.Client(),
		MediaPolicy: media.Policy{MaxImageBytes: 3},
	}, silentLogger())
	require.NoError(t, err)

	got := c.collectImageAttachments(context.Background(), mustJSON(t, map[string]any{
		"msgtype": "m.image",
		"url":     "mxc://matrix.org/pic123",
	}))
	assert.Nil(t, got)
}

func TestCollectImageAttachments_EnvelopeMIMELyingLogsAndDelivers(t *testing.T) {
	// Envelope claims JPEG, bytes are PNG. Sniffed value wins on the
	// Attachment.
	srv := mediaServer(t, pngHeader, http.StatusOK)
	defer srv.Close()

	c, err := New(Config{
		HomeserverURL: srv.URL, AccessToken: "tok", HTTPClient: srv.Client(),
	}, silentLogger())
	require.NoError(t, err)

	got := c.collectImageAttachments(context.Background(), mustJSON(t, map[string]any{
		"msgtype": "m.image",
		"url":     "mxc://matrix.org/pic123",
		"info":    map[string]string{"mimetype": "image/jpeg"},
	}))
	require.Len(t, got, 1)
	assert.Equal(t, "image/png", got[0].MediaType)
}

func TestCollectImageAttachments_MalformedContentReturnsNil(t *testing.T) {
	c, err := New(Config{HomeserverURL: "http://unused", AccessToken: "tok"}, silentLogger())
	require.NoError(t, err)
	got := c.collectImageAttachments(context.Background(), []byte("not json"))
	assert.Nil(t, got)
}

func TestExtractBody_MImageReturnsEmpty(t *testing.T) {
	// m.image body carries the filename by convention -- treating it
	// as text would forward noise. Assert empty.
	body := extractBody(mustJSON(t, map[string]any{
		"msgtype": "m.image",
		"body":    "IMG_0001.jpg",
	}))
	assert.Empty(t, body)
}
