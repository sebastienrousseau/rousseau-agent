package telegram

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/media"
)

var pngHeader = []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x00, 0x00, 0x0D}

// imageTestServer mirrors telegramTestServer but serves image bytes
// with the operator's chosen status. Any content-type is allowed —
// the pipeline sniffs from the bytes, not the header.
func imageTestServer(t *testing.T, filePath string, body []byte, status int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/getFile") {
			w.Header().Set("Content-Type", "application/json")
			resp := map[string]any{"result": map[string]any{"file_path": filePath}}
			blob, _ := json.Marshal(resp) //nolint:errcheck // static payload
			_, _ = w.Write(blob)          //nolint:errcheck // test writer
			return
		}
		if strings.Contains(r.URL.Path, "/file/bot") && strings.HasSuffix(r.URL.Path, "/"+filePath) {
			if status != http.StatusOK {
				w.WriteHeader(status)
				return
			}
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(body) //nolint:errcheck // test writer
			return
		}
		http.NotFound(w, r)
	}))
}

func telegramClient(t *testing.T, srv *httptest.Server, cfg Config) *Client {
	t.Helper()
	cfg.Token = "test"
	cfg.BaseURL = srv.URL
	c, err := New(cfg, silentLogger())
	require.NoError(t, err)
	return c
}

func TestCollectImageAttachments_PhotoBecomesAttachment(t *testing.T) {
	srv := imageTestServer(t, "photos/big.jpg", pngHeader, http.StatusOK)
	defer srv.Close()

	c := telegramClient(t, srv, Config{})
	handler := &captureHandler{}
	c.route(context.Background(), telegramUpdate{
		Message: &telegramMessage{
			Chat: telegramChat{ID: 42},
			Date: time.Now().Unix(),
			Photo: []telegramPhotoSize{
				{FileID: "small", Width: 90, Height: 90, FileSize: 4000},
				{FileID: "large", Width: 1280, Height: 720, FileSize: 240000},
			},
		},
	}, handler)

	require.Len(t, handler.msgs, 1)
	got := handler.msgs[0]
	assert.Empty(t, got.Body)
	require.Len(t, got.Attachments, 1)
	assert.Equal(t, "image/png", got.Attachments[0].MediaType)
	assert.Equal(t, pngHeader, got.Attachments[0].Data)
}

func TestCollectImageAttachments_CaptionBecomesBody(t *testing.T) {
	srv := imageTestServer(t, "photos/big.jpg", pngHeader, http.StatusOK)
	defer srv.Close()

	c := telegramClient(t, srv, Config{})
	handler := &captureHandler{}
	c.route(context.Background(), telegramUpdate{
		Message: &telegramMessage{
			Chat:    telegramChat{ID: 42},
			Date:    time.Now().Unix(),
			Caption: "what is this?",
			Photo: []telegramPhotoSize{
				{FileID: "large", Width: 640, Height: 480, FileSize: 90000},
			},
		},
	}, handler)

	require.Len(t, handler.msgs, 1)
	got := handler.msgs[0]
	assert.Equal(t, "what is this?", got.Body)
	require.Len(t, got.Attachments, 1)
}

func TestCollectImageAttachments_LargestSizeSelected(t *testing.T) {
	// The largest photo entry is what gets downloaded. Assert by
	// serving different bytes for "small" vs "large" and checking
	// which reached the handler.
	small := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x00, 0x00, 0x99}
	large := pngHeader // ends in ...0x00,0x0D
	// Serve either based on which file_id getFile returns.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/getFile") {
			var req struct {
				FileID string `json:"file_id"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req) //nolint:errcheck // static payload
			path := "photos/" + req.FileID + ".png"
			w.Header().Set("Content-Type", "application/json")
			resp := map[string]any{"result": map[string]any{"file_path": path}}
			blob, _ := json.Marshal(resp) //nolint:errcheck // static payload
			_, _ = w.Write(blob)          //nolint:errcheck // test writer
			return
		}
		if strings.HasSuffix(r.URL.Path, "/large.png") {
			_, _ = w.Write(large) //nolint:errcheck // test writer
			return
		}
		if strings.HasSuffix(r.URL.Path, "/small.png") {
			_, _ = w.Write(small) //nolint:errcheck // test writer
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	c := telegramClient(t, srv, Config{})
	handler := &captureHandler{}
	c.route(context.Background(), telegramUpdate{
		Message: &telegramMessage{
			Chat: telegramChat{ID: 42},
			Date: time.Now().Unix(),
			Photo: []telegramPhotoSize{
				{FileID: "small", Width: 90, Height: 90, FileSize: 4000},
				{FileID: "large", Width: 1280, Height: 720, FileSize: 240000},
			},
		},
	}, handler)

	require.Len(t, handler.msgs, 1)
	require.Len(t, handler.msgs[0].Attachments, 1)
	assert.Equal(t, large, handler.msgs[0].Attachments[0].Data,
		"the largest PhotoSize (last entry) is the one that gets downloaded")
}

func TestCollectImageAttachments_DownloadFailureDropsSilently(t *testing.T) {
	srv := imageTestServer(t, "photos/big.jpg", nil, http.StatusInternalServerError)
	defer srv.Close()

	c := telegramClient(t, srv, Config{})
	handler := &captureHandler{}
	c.route(context.Background(), telegramUpdate{
		Message: &telegramMessage{
			Chat:    telegramChat{ID: 42},
			Date:    time.Now().Unix(),
			Caption: "look",
			Photo: []telegramPhotoSize{
				{FileID: "large", Width: 640, Height: 480, FileSize: 90000},
			},
		},
	}, handler)

	require.Len(t, handler.msgs, 1)
	assert.Empty(t, handler.msgs[0].Attachments, "failed download drops the attachment, keeps the caption")
	assert.Equal(t, "look", handler.msgs[0].Body)
}

func TestCollectImageAttachments_OversizeIsDropped(t *testing.T) {
	srv := imageTestServer(t, "photos/big.jpg", pngHeader, http.StatusOK)
	defer srv.Close()

	c := telegramClient(t, srv, Config{
		MediaPolicy: media.Policy{MaxImageBytes: 3},
	})
	handler := &captureHandler{}
	c.route(context.Background(), telegramUpdate{
		Message: &telegramMessage{
			Chat:    telegramChat{ID: 42},
			Date:    time.Now().Unix(),
			Caption: "cap",
			Photo: []telegramPhotoSize{
				{FileID: "large", Width: 640, Height: 480, FileSize: 90000},
			},
		},
	}, handler)

	require.Len(t, handler.msgs, 1)
	assert.Empty(t, handler.msgs[0].Attachments)
	assert.Equal(t, "cap", handler.msgs[0].Body)
}

func TestCollectImageAttachments_EmptyFileIDSkipsDownload(t *testing.T) {
	// Empty file_id short-circuits before any HTTP.
	c := telegramClient(t, httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Fatal("no HTTP call should be made")
	})), Config{})
	got := c.collectImageAttachments(context.Background(), []telegramPhotoSize{{FileID: ""}})
	assert.Nil(t, got)
}

func TestCollectImageAttachments_EmptySliceReturnsNil(t *testing.T) {
	c := telegramClient(t, httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Fatal("no HTTP call should be made")
	})), Config{})
	assert.Nil(t, c.collectImageAttachments(context.Background(), nil))
}

func TestRoute_ImageOnlyWithoutCaptionStillRoutes(t *testing.T) {
	srv := imageTestServer(t, "photos/big.jpg", pngHeader, http.StatusOK)
	defer srv.Close()

	c := telegramClient(t, srv, Config{})
	handler := &captureHandler{}
	c.route(context.Background(), telegramUpdate{
		Message: &telegramMessage{
			Chat: telegramChat{ID: 42},
			Date: time.Now().Unix(),
			Photo: []telegramPhotoSize{
				{FileID: "large", Width: 640, Height: 480, FileSize: 90000},
			},
		},
	}, handler)

	require.Len(t, handler.msgs, 1, "an image-only message must reach the handler")
}
