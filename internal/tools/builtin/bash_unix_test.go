//go:build unix

package builtin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// bashTestOutputCap mirrors the 1 MiB capture cap; the marker allowance
// covers "\n[output truncated: N bytes dropped]".
const (
	bashTestOutputCap = 1 << 20
	bashTestMarkerMax = 64
)

func TestBash_OutputCappedDuringRun(t *testing.T) {
	tool := NewBashTool(30 * time.Second)
	in := json.RawMessage(`{"command": "head -c 20000000 /dev/zero | tr '\\0' a"}`)
	out, err := tool.Execute(context.Background(), in)
	require.NoError(t, err)
	assert.LessOrEqual(t, len(out), bashTestOutputCap+bashTestMarkerMax, "output exceeded the capture cap")
	assert.True(t, strings.Contains(out, "[output truncated:"), "missing truncation marker")
}

func TestBash_BackgroundChildKilledOnTimeout(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	tool := NewBashTool(300 * time.Millisecond)
	in, err := json.Marshal(bashInput{Command: "sleep 30 & echo $! > " + pidFile + "; sleep 30"})
	require.NoError(t, err)

	start := time.Now()
	_, err = tool.Execute(context.Background(), in)
	elapsed := time.Since(start)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "timed out")
	assert.Less(t, elapsed, 5*time.Second, "Execute blocked past its deadline")
	assertProcessGone(t, pidFile)
}

// assertProcessGone polls until the pid recorded in pidFile no longer
// exists (killed and reaped), failing after a short grace period.
func assertProcessGone(t *testing.T, pidFile string) {
	t.Helper()
	raw, err := os.ReadFile(pidFile) //nolint:gosec // test-owned temp path
	require.NoError(t, err)
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	require.NoError(t, err)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if syscall.Kill(pid, 0) != nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		t.Logf("cleanup kill %d: %v", pid, err)
	}
	t.Fatalf("background child %d survived the timeout", pid)
}
