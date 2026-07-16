package media

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// makePNG produces a 1×1 opaque PNG so tests can round-trip real
// bytes through the MIME sniffer.
func makePNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, color.NRGBA{R: 255, A: 255})
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	return buf.Bytes()
}

func makeJPEG(t *testing.T) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 8, 8))
	var buf bytes.Buffer
	require.NoError(t, jpeg.Encode(&buf, img, nil))
	return buf.Bytes()
}

func makeGIF(t *testing.T) []byte {
	t.Helper()
	img := image.NewPaletted(image.Rect(0, 0, 1, 1), color.Palette{color.Black, color.White})
	var buf bytes.Buffer
	require.NoError(t, gif.Encode(&buf, img, nil))
	return buf.Bytes()
}

func TestPolicy_AcceptsPNG(t *testing.T) {
	p := Policy{}
	mime, err := p.Accept(makePNG(t), 0)
	require.NoError(t, err)
	assert.Equal(t, "image/png", mime)
}

func TestPolicy_AcceptsJPEG(t *testing.T) {
	p := Policy{}
	mime, err := p.Accept(makeJPEG(t), 0)
	require.NoError(t, err)
	assert.Equal(t, "image/jpeg", mime)
}

func TestPolicy_AcceptsGIF(t *testing.T) {
	p := Policy{}
	mime, err := p.Accept(makeGIF(t), 0)
	require.NoError(t, err)
	assert.Equal(t, "image/gif", mime)
}

func TestPolicy_RejectsOversized(t *testing.T) {
	p := Policy{MaxImageBytes: 100}
	// Header sniffs as PNG but total > MaxImageBytes.
	data := append(makePNG(t), bytes.Repeat([]byte{0}, 200)...)
	_, err := p.Accept(data, 0)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrTooLarge))
}

func TestPolicy_RejectsOverTotalTurn(t *testing.T) {
	p := Policy{MaxTotalBytes: 100}
	_, err := p.Accept(makePNG(t), 90) // any accepted PNG is >10 bytes
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrTotalTooLarge))
}

func TestPolicy_RejectsUnknownMIME(t *testing.T) {
	p := Policy{}
	// Text bytes — sniffs as text/plain.
	_, err := p.Accept([]byte("this is plain text with enough bytes"), 0)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrDisallowedMIME))
}

func TestPolicy_ExplicitAllowlistNarrows(t *testing.T) {
	p := Policy{AllowedMIMEs: []string{"image/png"}}
	_, err := p.Accept(makeJPEG(t), 0)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrDisallowedMIME))
}

func TestPolicy_SniffedIgnoresParams(t *testing.T) {
	// Simulate a sniffer output that ends up with `; charset=utf-8`.
	assert.Equal(t, "image/png", stripParams("image/png; charset=binary"))
	assert.Equal(t, "text/plain", stripParams("text/plain"))
}

func TestPolicy_ZeroValueDefaults(t *testing.T) {
	p := Policy{}
	assert.Equal(t, DefaultMaxImageBytes, p.maxImage())
	assert.Equal(t, DefaultMaxTotalBytes, p.maxTotal())
	assert.Equal(t, DefaultAllowedMIMEs, p.allowed())
}
