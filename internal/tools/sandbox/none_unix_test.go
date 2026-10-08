//go:build unix

package sandbox_test

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/tools/sandbox"
)

// noneTestOutputCap mirrors the 1 MiB capture cap; the marker allowance
// covers "\n[output truncated: N bytes dropped]".
const (
	noneTestOutputCap = 1 << 20
	noneTestMarkerMax = 64
)

func TestNone_OutputCappedDuringRun(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := (&sandbox.None{}).Run(ctx, sandbox.Command{
		Path: "/bin/sh",
		Args: []string{"-c", "head -c 20000000 /dev/zero | tr '\\0' a"},
	})
	require.NoError(t, err)
	assert.LessOrEqual(t, len(res.CombinedOutput), noneTestOutputCap+noneTestMarkerMax, "output exceeded the capture cap")
	assert.True(t, strings.Contains(res.CombinedOutput, "[output truncated:"), "missing truncation marker")
}

func TestNone_BackgroundChildKilledOnTimeout(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := (&sandbox.None{}).Run(ctx, sandbox.Command{
		Path: "/bin/sh",
		Args: []string{"-c", "sleep 30 & echo $! > " + pidFile + "; sleep 30"},
	})
	elapsed := time.Since(start)

	require.Error(t, err)
	require.ErrorIs(t, ctx.Err(), context.DeadlineExceeded)
	assert.Less(t, elapsed, 5*time.Second, "Run blocked past its deadline")
	assertChildGone(t, pidFile)
}

// assertChildGone polls until the pid recorded in pidFile no longer
// exists (killed and reaped), failing after a short grace period.
func assertChildGone(t *testing.T, pidFile string) {
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
