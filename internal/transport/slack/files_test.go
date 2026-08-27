package slack

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/media"
	"github.com/sebastienrousseau/rousseau-agent/internal/transport"
)

// pngHeader is the 8-byte PNG magic + a few bytes of header. Enough
// for http.DetectContentType to return "image/png" without shipping
// a fixture image.
var pngHeader = []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x00, 0x00, 0x0D}

// fileServer stands in for Slack's authenticated file endpoint. It
// checks the bearer token, then serves `body` with the configured
// status. Path is used verbatim in test URLs.
type fileServer struct {
	srv         *httptest.Server
	wantToken   string
	body        []byte
	status      int
	seenAuth    string
	seenPath    string
	callCount   int
	failFirstN  int
	failWithErr bool
}

func newFileServer(t *testing.T, botToken string, body []byte) *fileServer {
	t.Helper()
	fs := &fileServer{wantToken: botToken, body: body, status: http.StatusOK}
	fs.srv = httptest.NewServer(http.HandlerFunc(fs.serve))
	t.Cleanup(fs.srv.Close)
	return fs
}

func (f *fileServer) serve(w http.ResponseWriter, r *http.Request) {
	f.callCount++
	f.seenAuth = r.Header.Get("Authorization")
	f.seenPath = r.URL.Path
	if f.failWithErr {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if strings.TrimPrefix(f.seenAuth, "Bearer ") != f.wantToken {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"ok":false,"error":"invalid_auth"}`))
		return
	}
	w.WriteHeader(f.status)
	_, _ = w.Write(f.body)
}

// eventEnvelope builds the events_api WebSocket frame for a single
// message with one image attachment served by fs.
func eventEnvelope(t *testing.T, text string, mimetype string, fs *fileServer) string {
	t.Helper()
	env := map[string]any{
		"type":        "events_api",
		"envelope_id": "e1",
		"payload": map[string]any{
			"event": map[string]any{
				"type":    "message",
				"subtype": "file_share",
				"user":    "U1",
				"text":    text,
				"channel": "C0",
				"files": []map[string]any{{
					"id":                   "F1",
					"mimetype":             mimetype,
					"size":                 len(fs.body),
					"url_private_download": fs.srv.URL + "/files/F1",
				}},
			},
		},
	}
	b, err := json.Marshal(env)
	require.NoError(t, err)
	return string(b)
}

func TestDispatchEvent_ImageFileBecomesAttachment(t *testing.T) {
	fs := newFileServer(t, "xoxb-y", pngHeader)

	c, err := New(Config{AppToken: "xapp-x", BotToken: "xoxb-y"}, silentLogger())
	require.NoError(t, err)

	var got transport.IncomingMessage
	handler := transport.HandlerFunc(func(_ context.Context, m transport.IncomingMessage) (string, error) {
		got = m
		return "", nil // empty reply keeps the postMessage side out of scope
	})
	envelope := eventEnvelope(t, "what is this?", "image/png", fs)
	ws := &fakeWS{}
	require.NoError(t, c.handleFrame(context.Background(), ws, []byte(envelope), handler))

	assert.Equal(t, "what is this?", got.Body)
	require.Len(t, got.Attachments, 1)
	assert.Equal(t, "image/png", got.Attachments[0].MediaType)
	assert.Equal(t, pngHeader, got.Attachments[0].Data)

	// The download went through with the bot token.
	assert.Equal(t, 1, fs.callCount)
	assert.Equal(t, "Bearer xoxb-y", fs.seenAuth)
	assert.Equal(t, "/files/F1", fs.seenPath)
}

func TestDispatchEvent_ImageOnlyStillRoutes(t *testing.T) {
	fs := newFileServer(t, "xoxb-y", pngHeader)
	c, err := New(Config{AppToken: "xapp-x", BotToken: "xoxb-y"}, silentLogger())
	require.NoError(t, err)

	var got transport.IncomingMessage
	handler := transport.HandlerFunc(func(_ context.Context, m transport.IncomingMessage) (string, error) {
		got = m
		return "", nil
	})
	envelope := eventEnvelope(t, "", "image/png", fs)
	ws := &fakeWS{}
	require.NoError(t, c.handleFrame(context.Background(), ws, []byte(envelope), handler))

	assert.Empty(t, got.Body)
	require.Len(t, got.Attachments, 1, "empty text + image must still reach the handler")
}

func TestDispatchEvent_NonImageFileIsSkipped(t *testing.T) {
	// PDF envelope MIME → the pipeline never opens the connection.
	// The file server must not be hit.
	fs := newFileServer(t, "xoxb-y", []byte{})
	c, err := New(Config{AppToken: "xapp-x", BotToken: "xoxb-y"}, silentLogger())
	require.NoError(t, err)

	envelope := eventEnvelope(t, "here", "application/pdf", fs)
	ws := &fakeWS{}
	var got transport.IncomingMessage
	handler := transport.HandlerFunc(func(_ context.Context, m transport.IncomingMessage) (string, error) {
		got = m
		return "", nil
	})
	require.NoError(t, c.handleFrame(context.Background(), ws, []byte(envelope), handler))
	assert.Equal(t, 0, fs.callCount, "non-image envelope MIME must not trigger a download")
	assert.Empty(t, got.Attachments)
	assert.Equal(t, "here", got.Body, "text-only body still routes")
}

func TestDispatchEvent_FileDownloadFailureIsSkipped(t *testing.T) {
	fs := newFileServer(t, "xoxb-y", nil)
	fs.failWithErr = true
	c, err := New(Config{AppToken: "xapp-x", BotToken: "xoxb-y"}, silentLogger())
	require.NoError(t, err)

	envelope := eventEnvelope(t, "with pic", "image/png", fs)
	ws := &fakeWS{}
	var got transport.IncomingMessage
	handler := transport.HandlerFunc(func(_ context.Context, m transport.IncomingMessage) (string, error) {
		got = m
		return "", nil
	})
	require.NoError(t, c.handleFrame(context.Background(), ws, []byte(envelope), handler))
	assert.Empty(t, got.Attachments, "500 from Slack drops the attachment, keeps the text")
	assert.Equal(t, "with pic", got.Body)
}

func TestDispatchEvent_OversizeFileIsDropped(t *testing.T) {
	fs := newFileServer(t, "xoxb-y", pngHeader)
	c, err := New(Config{
		AppToken: "xapp-x", BotToken: "xoxb-y",
		MediaPolicy: media.Policy{MaxImageBytes: 3},
	}, silentLogger())
	require.NoError(t, err)

	envelope := eventEnvelope(t, "small caps", "image/png", fs)
	ws := &fakeWS{}
	var got transport.IncomingMessage
	handler := transport.HandlerFunc(func(_ context.Context, m transport.IncomingMessage) (string, error) {
		got = m
		return "", nil
	})
	require.NoError(t, c.handleFrame(context.Background(), ws, []byte(envelope), handler))
	assert.Empty(t, got.Attachments)
	assert.Equal(t, "small caps", got.Body)
}

func TestDispatchEvent_FileShareSubtypeAdmitted(t *testing.T) {
	// file_share is the subtype Slack tags plain user attachments
	// with. Before this PR it was ignored (treated the same as
	// channel_join). Verifies the new admit path.
	fs := newFileServer(t, "xoxb-y", pngHeader)
	c, err := New(Config{AppToken: "xapp-x", BotToken: "xoxb-y"}, silentLogger())
	require.NoError(t, err)

	envelope := eventEnvelope(t, "", "image/png", fs)
	ws := &fakeWS{}
	called := false
	handler := transport.HandlerFunc(func(context.Context, transport.IncomingMessage) (string, error) {
		called = true
		return "", nil
	})
	require.NoError(t, c.handleFrame(context.Background(), ws, []byte(envelope), handler))
	assert.True(t, called)
}

func TestDispatchEvent_FileWithoutURLIsSkipped(t *testing.T) {
	env := fmt.Sprintf(`{"type":"events_api","envelope_id":"e1","payload":{"event":{"type":"message","subtype":"file_share","user":"U1","text":"hi","channel":"C0","files":[{"id":"F1","mimetype":"image/png"}]}}}`)
	c, err := New(Config{AppToken: "xapp-x", BotToken: "xoxb-y"}, silentLogger())
	require.NoError(t, err)

	ws := &fakeWS{}
	var got transport.IncomingMessage
	handler := transport.HandlerFunc(func(_ context.Context, m transport.IncomingMessage) (string, error) {
		got = m
		return "", nil
	})
	require.NoError(t, c.handleFrame(context.Background(), ws, []byte(env), handler))
	assert.Empty(t, got.Attachments, "missing url_private_download → skip that file")
	assert.Equal(t, "hi", got.Body)
}

func TestIsImageMIME(t *testing.T) {
	assert.True(t, isImageMIME("image/png"))
	assert.True(t, isImageMIME("image/jpeg"))
	assert.False(t, isImageMIME("application/pdf"))
	assert.False(t, isImageMIME(""))
	assert.False(t, isImageMIME("image"))
}

func TestDownloadFile_UsesBotToken(t *testing.T) {
	fs := newFileServer(t, "xoxb-y", []byte("payload"))
	c, err := New(Config{AppToken: "xapp-x", BotToken: "xoxb-y"}, silentLogger())
	require.NoError(t, err)

	body, err := c.downloadFile(context.Background(), fs.srv.URL+"/files/F1")
	require.NoError(t, err)
	assert.Equal(t, []byte("payload"), body)
	assert.Equal(t, "Bearer xoxb-y", fs.seenAuth)
}

func TestDownloadFile_HTTPErrorSurfaces(t *testing.T) {
	fs := newFileServer(t, "xoxb-y", nil)
	fs.status = http.StatusUnauthorized
	c, err := New(Config{AppToken: "xapp-x", BotToken: "xoxb-y"}, silentLogger())
	require.NoError(t, err)

	_, err = c.downloadFile(context.Background(), fs.srv.URL+"/files/F1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "HTTP 401")
}

func TestDownloadFile_BadURLReturnsError(t *testing.T) {
	c, err := New(Config{AppToken: "xapp-x", BotToken: "xoxb-y"}, silentLogger())
	require.NoError(t, err)
	_, err = c.downloadFile(context.Background(), "://bad-url")
	require.Error(t, err)
}

func TestDispatchEvent_EnvelopeMIMELyingLogsAndDelivers(t *testing.T) {
	// Envelope claims JPEG, bytes are PNG. Slack file endpoint returns
	// the PNG bytes; the pipeline must land the sniffed value on the
	// attachment (PNG), not the envelope's claim.
	fs := newFileServer(t, "xoxb-y", pngHeader)
	c, err := New(Config{AppToken: "xapp-x", BotToken: "xoxb-y"}, silentLogger())
	require.NoError(t, err)

	envelope := eventEnvelope(t, "", "image/jpeg", fs)
	ws := &fakeWS{}
	var got transport.IncomingMessage
	handler := transport.HandlerFunc(func(_ context.Context, m transport.IncomingMessage) (string, error) {
		got = m
		return "", nil
	})
	require.NoError(t, c.handleFrame(context.Background(), ws, []byte(envelope), handler))
	require.Len(t, got.Attachments, 1)
	assert.Equal(t, "image/png", got.Attachments[0].MediaType,
		"sniffed MIME must win over the envelope's claim")
}
