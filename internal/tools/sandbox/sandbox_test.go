package sandbox_test

import (
	"context"
	"errors"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/tools/sandbox"
)

func TestNew_ReturnsExpectedBackendByKind(t *testing.T) {
	for _, tc := range []struct {
		kind, want string
	}{
		{"", "none"},
		{"none", "none"},
		{"gvisor", "gvisor"},
		{"nsjail", "nsjail"},
		{"firecracker", "firecracker"},
	} {
		b, err := sandbox.New(tc.kind)
		require.NoErrorf(t, err, "kind=%q", tc.kind)
		assert.Equalf(t, tc.want, b.Kind(), "kind=%q", tc.kind)
	}
}

func TestNew_UnknownKindErrors(t *testing.T) {
	_, err := sandbox.New("wat")
	assert.Error(t, err)
}

func TestNone_RunsCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX-only fixture")
	}
	s := &sandbox.None{}
	res, err := s.Run(context.Background(), sandbox.Command{
		Path: "/bin/sh",
		Args: []string{"-c", "printf hello"},
	})
	require.NoError(t, err)
	assert.Equal(t, "hello", res.CombinedOutput)
	assert.Equal(t, 0, res.ExitCode)
}

func TestNone_CapturesStderr(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX-only fixture")
	}
	s := &sandbox.None{}
	res, err := s.Run(context.Background(), sandbox.Command{
		Path: "/bin/sh",
		Args: []string{"-c", "echo out; echo err 1>&2"},
	})
	require.NoError(t, err)
	// CombinedOutput merges stderr — matches pre-sandbox bash tool.
	assert.Contains(t, res.CombinedOutput, "out")
	assert.Contains(t, res.CombinedOutput, "err")
}

func TestNone_NonZeroExitCodeSurfaced(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX-only fixture")
	}
	s := &sandbox.None{}
	res, err := s.Run(context.Background(), sandbox.Command{
		Path: "/bin/sh",
		Args: []string{"-c", "exit 7"},
	})
	assert.Error(t, err)
	assert.Equal(t, 7, res.ExitCode)
}

func TestNone_MissingPathReturnsError(t *testing.T) {
	s := &sandbox.None{}
	_, err := s.Run(context.Background(), sandbox.Command{})
	assert.Error(t, err)
}

func TestGVisor_MissingRuntimeReturnsUnavailable(t *testing.T) {
	g := &sandbox.GVisor{Binary: "does-not-exist-runsc-binary"}
	_, err := g.Run(context.Background(), sandbox.Command{Path: "/bin/true"})
	assert.ErrorIs(t, err, sandbox.ErrUnavailable)
}

func TestNSJail_MissingRuntimeReturnsUnavailable(t *testing.T) {
	n := &sandbox.NSJail{Binary: "does-not-exist-nsjail-binary"}
	_, err := n.Run(context.Background(), sandbox.Command{Path: "/bin/true"})
	assert.ErrorIs(t, err, sandbox.ErrUnavailable)
}

func TestFirecracker_AlwaysUnavailable(t *testing.T) {
	f, err := sandbox.New("firecracker")
	require.NoError(t, err)
	_, err = f.Run(context.Background(), sandbox.Command{Path: "/bin/true"})
	assert.True(t, errors.Is(err, sandbox.ErrUnavailable), "firecracker scaffold must return ErrUnavailable")
}
