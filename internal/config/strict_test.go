package config

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLoad_RejectsUnknownKeys pins strict decoding: a misspelt key
// used to be ignored, leaving the default in force with no warning.
func TestLoad_RejectsUnknownKeys(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	path := filepath.Join(t.TempDir(), "cfg.yaml")
	require.NoError(t, os.WriteFile(path, []byte("agent:\n  aprover:\n    mode: deny_all\n"), 0o600))
	_, err := Load(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "aprover")
	assert.Contains(t, err.Error(), envAllowUnknownKeys, "the error names the opt-out")
}

// TestREADMEConfigExamplesLoad keeps the documented configuration
// honest: every ```yaml block in the README must load under strict
// decoding. It caught a section documenting skill-signing keys that
// did not exist.
func TestREADMEConfigExamplesLoad(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	readme, err := os.ReadFile(filepath.Join("..", "..", "README.md"))
	require.NoError(t, err)
	blocks := regexp.MustCompile("(?s)```yaml\n(.*?)```").FindAllSubmatch(readme, -1)
	require.NotEmpty(t, blocks)
	for i, b := range blocks {
		path := filepath.Join(t.TempDir(), "example.yaml")
		require.NoError(t, os.WriteFile(path, b[1], 0o600))
		_, err := Load(path)
		assert.NoError(t, err, "README yaml block %d", i+1)
	}
}
