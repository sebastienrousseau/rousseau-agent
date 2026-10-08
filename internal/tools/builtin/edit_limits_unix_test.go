//go:build unix

package builtin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/tools/fsguard"
)

func editReq(t *testing.T, path string) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(map[string]string{"path": path, "old_string": "a", "new_string": "b"})
	require.NoError(t, err)
	return b
}

// edit refuses a FIFO at once and a file over the read cap.
func TestEdit_RefusesFIFOAndOversizedFile(t *testing.T) {
	ws := t.TempDir()
	g, err := fsguard.New(ws, nil)
	require.NoError(t, err)
	e := NewEditTool()
	e.Guard = g

	fifo := filepath.Join(ws, "pipe")
	require.NoError(t, syscall.Mkfifo(fifo, 0o600))
	done := make(chan error, 1)
	go func() { _, err := e.Execute(context.Background(), editReq(t, fifo)); done <- err }()
	select {
	case err := <-done:
		assert.Error(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("edit blocked on a FIFO")
	}

	big := filepath.Join(ws, "big.txt")
	require.NoError(t, os.WriteFile(big, []byte(strings.Repeat("a", int(defaultReadMaxBytes)+1)), 0o600))
	_, err = e.Execute(context.Background(), editReq(t, big))
	assert.ErrorContains(t, err, "limit")
}
