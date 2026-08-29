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

func TestDefaultPolicy_NoNetworkOn(t *testing.T) {
	// The shipped default is the safe disposition: deny egress.
	// Callers that need a network-enabled sandbox override
	// NoNetwork explicitly, so a code-review can spot it.
	p := sandbox.DefaultPolicy()
	assert.True(t, p.NoNetwork, "DefaultPolicy must deny egress by default")
	assert.Zero(t, p.CPUSeconds, "no CPU cap by default (caller's ctx bounds it)")
	assert.Zero(t, p.MemoryBytes, "no memory cap by default")
	assert.Empty(t, p.TmpdirRoot, "empty falls back to os.TempDir()")
}

func TestNewWithPolicy_ThreadsPolicyThroughToBackend(t *testing.T) {
	pol := sandbox.Policy{NoNetwork: true, CPUSeconds: 42}
	// gvisor + nsjail carry the Policy; none + firecracker don't
	// need it (None is a straight exec; firecracker isn't
	// implemented).
	g, err := sandbox.NewWithPolicy("gvisor", pol)
	require.NoError(t, err)
	gv, ok := g.(*sandbox.GVisor)
	require.True(t, ok, "gvisor backend must be *GVisor")
	assert.Equal(t, pol, gv.Policy)

	n, err := sandbox.NewWithPolicy("nsjail", pol)
	require.NoError(t, err)
	ns, ok := n.(*sandbox.NSJail)
	require.True(t, ok, "nsjail backend must be *NSJail")
	assert.Equal(t, pol, ns.Policy)
}

func TestNewWithPolicy_UnknownKind(t *testing.T) {
	_, err := sandbox.NewWithPolicy("wat", sandbox.Policy{})
	assert.Error(t, err)
}
