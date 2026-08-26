package signal

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeTranscriber struct {
	mu       sync.Mutex
	audio    []byte
	mimetype string
	reply    string
	err      error
}

func (f *fakeTranscriber) Transcribe(_ context.Context, audio []byte, mimetype string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.audio = append([]byte(nil), audio...)
	f.mimetype = mimetype
	return f.reply, f.err
}

func TestTranscribeAudio_NilTranscriberIsNoop(t *testing.T) {
	c, err := New(Config{Account: "+1"}, silentLogger())
	require.NoError(t, err)
	got := c.transcribeAudio(context.Background(), []receiveAttachment{
		{ID: "any", ContentType: "audio/mp4"},
	})
	assert.Empty(t, got)
}

func TestTranscribeAudio_EmptyAttachmentsDirIsNoop(t *testing.T) {
	c, err := New(Config{Account: "+1", Transcriber: &fakeTranscriber{reply: "no"}}, silentLogger())
	require.NoError(t, err)
	got := c.transcribeAudio(context.Background(), []receiveAttachment{
		{ID: "any", ContentType: "audio/mp4"},
	})
	assert.Empty(t, got, "AttachmentsDir empty → no I/O, no transcript")
}

func TestTranscribeAudio_HappyPath(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "att-1"), []byte("AUDIO"), 0o600))

	ft := &fakeTranscriber{reply: "hello"}
	c, err := New(Config{
		Account:        "+1",
		Transcriber:    ft,
		AttachmentsDir: dir,
	}, silentLogger())
	require.NoError(t, err)

	got := c.transcribeAudio(context.Background(), []receiveAttachment{
		{ID: "att-1", ContentType: "audio/aac"},
	})
	assert.Equal(t, "hello", got)
	assert.Equal(t, []byte("AUDIO"), ft.audio)
	assert.Equal(t, "audio/aac", ft.mimetype)
}

func TestTranscribeAudio_NonAudioAttachmentIsSkipped(t *testing.T) {
	dir := t.TempDir()
	ft := &fakeTranscriber{reply: "should-not-fire"}
	c, err := New(Config{Account: "+1", Transcriber: ft, AttachmentsDir: dir}, silentLogger())
	require.NoError(t, err)
	got := c.transcribeAudio(context.Background(), []receiveAttachment{
		{ID: "x", ContentType: "image/jpeg"},
	})
	assert.Empty(t, got)
	assert.Empty(t, ft.mimetype, "transcriber not invoked for non-audio")
}

func TestTranscribeAudio_MissingFileReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	ft := &fakeTranscriber{reply: "would be lost"}
	c, err := New(Config{Account: "+1", Transcriber: ft, AttachmentsDir: dir}, silentLogger())
	require.NoError(t, err)
	got := c.transcribeAudio(context.Background(), []receiveAttachment{
		{ID: "missing", ContentType: "audio/ogg"},
	})
	assert.Empty(t, got)
}

func TestTranscribeAudio_MaxBytesRespected(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "big"), make([]byte, 100), 0o600))

	ft := &fakeTranscriber{reply: "ok"}
	c, err := New(Config{
		Account: "+1", Transcriber: ft, AttachmentsDir: dir, MaxAudioBytes: 10,
	}, silentLogger())
	require.NoError(t, err)

	_ = c.transcribeAudio(context.Background(), []receiveAttachment{
		{ID: "big", ContentType: "audio/mp4"},
	})
	assert.Len(t, ft.audio, 10, "read must be truncated to MaxAudioBytes")
}

func TestTranscribeAudio_MissingIDIsSkipped(t *testing.T) {
	dir := t.TempDir()
	ft := &fakeTranscriber{reply: "no"}
	c, err := New(Config{Account: "+1", Transcriber: ft, AttachmentsDir: dir}, silentLogger())
	require.NoError(t, err)
	got := c.transcribeAudio(context.Background(), []receiveAttachment{
		{ContentType: "audio/aac"}, // no ID
	})
	assert.Empty(t, got)
}
