package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFirstNonEmpty(t *testing.T) {
	assert.Equal(t, "a", firstNonEmpty("", "a", "b"))
	assert.Equal(t, "b", firstNonEmpty("", "", "b"))
	assert.Equal(t, "", firstNonEmpty("", "", ""))
}

func TestWhatsappLogLevel(t *testing.T) {
	assert.Equal(t, "DEBUG", whatsappLogLevel("debug"))
	assert.Equal(t, "WARN", whatsappLogLevel("warn"))
	assert.Equal(t, "WARN", whatsappLogLevel("warning"))
	assert.Equal(t, "ERROR", whatsappLogLevel("error"))
	assert.Equal(t, "INFO", whatsappLogLevel("info"))
	assert.Equal(t, "INFO", whatsappLogLevel("mystery"))
}

func TestResolveWhatsAppDSN_DefaultsToHome(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dsn, err := resolveWhatsAppDSN("")
	require.NoError(t, err)
	assert.Contains(t, dsn, "file:")
	assert.Contains(t, dsn, "whatsapp.db")
	assert.Contains(t, dsn, "busy_timeout(15000)")
}

func TestResolveWhatsAppDSN_ExplicitPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "explicit", "wa.db")
	dsn, err := resolveWhatsAppDSN(path)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(dsn, "file:"+path))
	// Directory should have been created.
	_, err = os.Stat(filepath.Dir(path))
	require.NoError(t, err)
}
