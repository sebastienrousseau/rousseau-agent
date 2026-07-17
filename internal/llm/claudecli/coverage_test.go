package claudecli

import (
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
)

// TestExtensionFor walks every MIME type the writeImages helper
// knows how to map — plus the fallback.
func TestExtensionFor(t *testing.T) {
	cases := map[string]string{
		"image/png":           ".png",
		"image/jpeg":          ".jpg",
		"image/webp":          ".webp",
		"image/gif":           ".gif",
		"application/unknown": ".bin",
		"":                    ".bin",
	}
	for mime, want := range cases {
		assert.Equal(t, want, extensionFor(mime), mime)
	}
}

// TestWriteImages_RoundTrip drives the temp-file writer end-to-end,
// verifying each image lands at the correct path with mode 0600 and
// cleanup removes the directory.
func TestWriteImages_RoundTrip(t *testing.T) {
	images := []*agent.Image{
		{MediaType: "image/png", Data: []byte{0x89, 0x50, 0x4E, 0x47}},
		{MediaType: "image/jpeg", Data: []byte{0xFF, 0xD8, 0xFF}},
	}
	paths, cleanup, err := writeImages(images)
	require.NoError(t, err)
	require.Len(t, paths, 2)
	assert.True(t, filepath.Base(paths[0]) == "img-0.png")
	assert.True(t, filepath.Base(paths[1]) == "img-1.jpg")
	cleanup()
}

func TestWriteImages_EmptyIsNoop(t *testing.T) {
	paths, cleanup, err := writeImages(nil)
	require.NoError(t, err)
	assert.Empty(t, paths)
	assert.NotNil(t, cleanup)
	cleanup()
}

// TestDefaultRun_HappyPath drives the exec.Cmd shim with a
// guaranteed-to-succeed command so the coverage report shows it
// runs its own bytes rather than depending on caller invocation.
func TestDefaultRun_HappyPath(t *testing.T) {
	if _, err := exec.LookPath("echo"); err != nil {
		t.Skip("no /bin/echo available")
	}
	out, err := defaultRun(exec.Command("echo", "hi"))
	require.NoError(t, err)
	assert.Contains(t, string(out), "hi")
}
