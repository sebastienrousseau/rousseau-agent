package audio

import (
	"bytes"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// errSink is a sentinel so tests can assert the writer's error is
// propagated rather than replaced.
var errSink = errors.New("sink failed")

// failOnNthWrite succeeds for the first n-1 Write calls and fails on
// the nth.
//
// Failing on a write *count* rather than a byte offset is deliberate.
// multipart.Writer generates a random boundary, so the exact byte
// offset of each part header shifts between runs; a byte-offset
// fixture would be brittle for no benefit. Write calls, by contrast,
// map one-to-one onto the steps of writeTranscriptionForm.
type failOnNthWrite struct {
	n     int
	calls int
}

func (f *failOnNthWrite) Write(p []byte) (int, error) {
	f.calls++
	if f.calls >= f.n {
		return 0, errSink
	}
	return len(p), nil
}

// countingWriter records how many Write calls a full successful run
// makes, so the failure table below covers every one of them.
type countingWriter struct{ calls int }

func (c *countingWriter) Write(p []byte) (int, error) {
	c.calls++
	return len(p), nil
}

func TestWriteTranscriptionForm_Success(t *testing.T) {
	var buf bytes.Buffer
	audio := []byte("fake-ogg-bytes")

	ct, err := writeTranscriptionForm(&buf, audio, "audio/ogg", "whisper-1", "fr")
	require.NoError(t, err)

	// The returned Content-Type must carry the boundary actually used,
	// otherwise the server cannot parse the body we just built.
	mediaType, params, err := mime.ParseMediaType(ct)
	require.NoError(t, err)
	assert.Equal(t, "multipart/form-data", mediaType)
	require.NotEmpty(t, params["boundary"])

	// Round-trip the body back through a reader and assert the wire
	// contents, rather than asserting on how they were produced.
	mr := multipart.NewReader(&buf, params["boundary"])
	got := map[string]string{}
	var filename string
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err)
		body, err := io.ReadAll(part)
		require.NoError(t, err)
		if part.FormName() == "file" {
			filename = part.FileName()
		}
		got[part.FormName()] = string(body)
	}

	assert.Equal(t, string(audio), got["file"], "audio bytes must reach the wire intact")
	assert.Equal(t, "whisper-1", got["model"])
	assert.Equal(t, "json", got["response_format"])
	assert.Equal(t, "fr", got["language"])
	assert.Equal(t, "voice.ogg", filename, "extension must follow the mime type")
}

func TestWriteTranscriptionForm_OmitsLanguageWhenEmpty(t *testing.T) {
	var buf bytes.Buffer
	ct, err := writeTranscriptionForm(&buf, []byte("x"), "audio/ogg", "whisper-1", "")
	require.NoError(t, err)

	_, params, err := mime.ParseMediaType(ct)
	require.NoError(t, err)

	mr := multipart.NewReader(&buf, params["boundary"])
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err)
		assert.NotEqual(t, "language", part.FormName(),
			"an empty language must not be sent as a blank field")
	}
}

// TestWriteTranscriptionForm_PropagatesWriterFailure walks a failing
// sink across every write the function performs.
//
// The property under test is that a partial write is never silently
// swallowed: whichever step fails, the caller gets a non-nil error
// wrapping the sink's error, and never a truncated body presented as
// success. Against the bytes.Buffer this code used to own, none of
// these branches could fire at all.
func TestWriteTranscriptionForm_PropagatesWriterFailure(t *testing.T) {
	// Discover how many writes a successful run makes so the loop
	// below cannot silently under-cover if the body layout changes.
	var counter countingWriter
	_, err := writeTranscriptionForm(&counter, []byte("fake-ogg-bytes"), "audio/ogg", "whisper-1", "fr")
	require.NoError(t, err)
	require.Greater(t, counter.calls, 1, "expected multiple writes to fail across")

	for n := 1; n <= counter.calls; n++ {
		t.Run("fail_on_write_"+string(rune('0'+n)), func(t *testing.T) {
			sink := &failOnNthWrite{n: n}
			ct, err := writeTranscriptionForm(sink, []byte("fake-ogg-bytes"), "audio/ogg", "whisper-1", "fr")
			require.Error(t, err, "write %d failing must surface an error", n)
			assert.ErrorIs(t, err, errSink, "the sink's error must be wrapped, not replaced")
			assert.Empty(t, ct, "no Content-Type may be returned when the body is incomplete")
			assert.True(t, strings.HasPrefix(err.Error(), "audio/openai: "),
				"errors must be attributed to this package, got %q", err.Error())
		})
	}
}
