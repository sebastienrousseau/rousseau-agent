package signal

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pathIDFixture lays out a temp tree in which each hostile id names a
// real file when joined onto the attachments dir, so an unchecked id
// would be read:
//
//	<base>/etc/passwd          reached by "../../etc/passwd"
//	<base>/x/y/                AttachmentsDir
//	<base>/x/y/a/b             reached by "a/b"
func pathIDFixture(t *testing.T, body []byte) string {
	t.Helper()
	base := t.TempDir()
	dir := filepath.Join(base, "x", "y")
	for _, p := range []string{filepath.Join(base, "etc", "passwd"), filepath.Join(dir, "a", "b")} {
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o700))
		require.NoError(t, os.WriteFile(p, body, 0o600))
	}
	return dir
}

var hostileIDs = []string{"../../etc/passwd", "a/b", ".", ".."}

func TestCollectImageAttachments_RefusesPathIDs(t *testing.T) {
	dir := pathIDFixture(t, pngHeader)
	c := signalClient(t, Config{AttachmentsDir: dir})
	for _, id := range hostileIDs {
		got := c.collectImageAttachments([]receiveAttachment{{ID: id, ContentType: "image/png"}})
		assert.Empty(t, got, "id %q must not be read", id)
	}
}

func TestTranscribeAudio_RefusesPathIDs(t *testing.T) {
	dir := pathIDFixture(t, []byte("audio"))
	tr := &fakeTranscriber{reply: "transcript"}
	c := signalClient(t, Config{AttachmentsDir: dir, Transcriber: tr})
	for _, id := range hostileIDs {
		got := c.transcribeAudio(context.Background(), []receiveAttachment{{ID: id, ContentType: "audio/aac"}})
		assert.Empty(t, got, "id %q must not be read", id)
	}
	assert.Nil(t, tr.audio, "transcriber must never be called")
}

func TestValidAttachmentID(t *testing.T) {
	assert.True(t, validAttachmentID("aBc123.png"))
	for _, id := range append(hostileIDs, "", `a\b`) {
		assert.False(t, validAttachmentID(id), "id %q", id)
	}
}
