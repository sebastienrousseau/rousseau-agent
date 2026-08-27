package signal

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/media"
	"github.com/sebastienrousseau/rousseau-agent/internal/transport"
)

var pngHeader = []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x00, 0x00, 0x0D}

// attachmentDir writes fixture bytes to <dir>/<id> and returns dir.
// Signal-cli exposes attachments only by id and expects the caller
// to read from the shared directory it wrote to.
func attachmentDir(t *testing.T, files map[string][]byte) string {
	t.Helper()
	dir := t.TempDir()
	for id, body := range files {
		require.NoError(t, os.WriteFile(filepath.Join(dir, id), body, 0o600))
	}
	return dir
}

func signalClient(t *testing.T, cfg Config) *Client {
	t.Helper()
	if cfg.Account == "" {
		cfg.Account = "+15551234567"
	}
	c, err := New(cfg, silentLogger())
	require.NoError(t, err)
	return c
}

func TestCollectImageAttachments_ReadsFromAttachmentsDir(t *testing.T) {
	dir := attachmentDir(t, map[string][]byte{"file-1": pngHeader})
	c := signalClient(t, Config{AttachmentsDir: dir})

	got := c.collectImageAttachments([]receiveAttachment{{
		ID:          "file-1",
		ContentType: "image/png",
	}})
	require.Len(t, got, 1)
	assert.Equal(t, "image/png", got[0].MediaType)
	assert.Equal(t, pngHeader, got[0].Data)
}

func TestCollectImageAttachments_NonImageContentTypeSkipped(t *testing.T) {
	dir := attachmentDir(t, map[string][]byte{"doc-1": []byte("pdfbytes")})
	c := signalClient(t, Config{AttachmentsDir: dir})

	got := c.collectImageAttachments([]receiveAttachment{{
		ID:          "doc-1",
		ContentType: "application/pdf",
	}})
	assert.Empty(t, got)
}

func TestCollectImageAttachments_EmptyAttachmentsDirDisables(t *testing.T) {
	// No AttachmentsDir configured → no ingestion path.
	c := signalClient(t, Config{})
	got := c.collectImageAttachments([]receiveAttachment{{
		ID: "x", ContentType: "image/png",
	}})
	assert.Nil(t, got)
}

func TestCollectImageAttachments_EmptyIDLoggedAndSkipped(t *testing.T) {
	dir := attachmentDir(t, map[string][]byte{})
	c := signalClient(t, Config{AttachmentsDir: dir})
	got := c.collectImageAttachments([]receiveAttachment{{
		ID:          "",
		ContentType: "image/png",
		Filename:    "foo.png",
	}})
	assert.Empty(t, got)
}

func TestCollectImageAttachments_MissingFileDropped(t *testing.T) {
	dir := attachmentDir(t, map[string][]byte{})
	c := signalClient(t, Config{AttachmentsDir: dir})
	got := c.collectImageAttachments([]receiveAttachment{{
		ID:          "missing",
		ContentType: "image/png",
	}})
	assert.Empty(t, got, "missing file must be dropped, not surfaced")
}

func TestCollectImageAttachments_OversizeIsDropped(t *testing.T) {
	dir := attachmentDir(t, map[string][]byte{"file-1": pngHeader})
	c := signalClient(t, Config{
		AttachmentsDir: dir,
		MediaPolicy:    media.Policy{MaxImageBytes: 3},
	})
	got := c.collectImageAttachments([]receiveAttachment{{
		ID:          "file-1",
		ContentType: "image/png",
	}})
	assert.Empty(t, got)
}

func TestCollectImageAttachments_EnvelopeMIMELyingLogsAndDelivers(t *testing.T) {
	dir := attachmentDir(t, map[string][]byte{"file-1": pngHeader})
	c := signalClient(t, Config{AttachmentsDir: dir})
	got := c.collectImageAttachments([]receiveAttachment{{
		ID:          "file-1",
		ContentType: "image/jpeg", // envelope lies
	}})
	require.Len(t, got, 1)
	assert.Equal(t, "image/png", got[0].MediaType,
		"sniffed MIME must win over the envelope's claim")
}

func TestCollectImageAttachments_MultipleFilesFolded(t *testing.T) {
	dir := attachmentDir(t, map[string][]byte{
		"a": pngHeader,
		"b": pngHeader,
	})
	c := signalClient(t, Config{AttachmentsDir: dir})
	got := c.collectImageAttachments([]receiveAttachment{
		{ID: "a", ContentType: "image/png"},
		{ID: "b", ContentType: "image/png"},
	})
	require.Len(t, got, 2)
}

func TestHandleFrame_ImageAttachmentRoutesWithoutText(t *testing.T) {
	dir := attachmentDir(t, map[string][]byte{"file-1": pngHeader})
	c := signalClient(t, Config{AttachmentsDir: dir})

	var got transport.IncomingMessage
	handler := transport.HandlerFunc(func(_ context.Context, m transport.IncomingMessage) (string, error) {
		got = m
		return "", nil
	})
	frame := []byte(`{"jsonrpc":"2.0","method":"receive","params":{"envelope":{"source":"+15551112222","sourceNumber":"+15551112222","timestamp":1700000000000,"dataMessage":{"message":"","attachments":[{"id":"file-1","contentType":"image/png"}]}},"account":"+15551234567"}}`)
	require.NoError(t, c.handleFrame(context.Background(), frame, handler))
	assert.Equal(t, "+15551112222", got.From)
	assert.Empty(t, got.Body)
	require.Len(t, got.Attachments, 1)
}

func TestHandleFrame_ImageAttachmentAndTextTogether(t *testing.T) {
	dir := attachmentDir(t, map[string][]byte{"file-1": pngHeader})
	c := signalClient(t, Config{AttachmentsDir: dir})

	var got transport.IncomingMessage
	handler := transport.HandlerFunc(func(_ context.Context, m transport.IncomingMessage) (string, error) {
		got = m
		return "", nil
	})
	frame := []byte(`{"jsonrpc":"2.0","method":"receive","params":{"envelope":{"source":"+15551112222","sourceNumber":"+15551112222","timestamp":1700000000000,"dataMessage":{"message":"what is this?","attachments":[{"id":"file-1","contentType":"image/png"}]}},"account":"+15551234567"}}`)
	require.NoError(t, c.handleFrame(context.Background(), frame, handler))
	assert.Equal(t, "what is this?", got.Body)
	require.Len(t, got.Attachments, 1)
}
