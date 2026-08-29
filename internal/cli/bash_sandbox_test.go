package cli

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/config"
	"github.com/sebastienrousseau/rousseau-agent/internal/tools/sandbox"
)

func TestBuildBashTool_EmptyConfigMatchesLegacyDefault(t *testing.T) {
	// The pre-config path built the tool with a 60s timeout and no
	// sandbox. An operator upgrading with no config change gets
	// exactly that back.
	got, err := buildBashTool(config.BashConfig{})
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, defaultBashTimeout, got.Timeout)
	assert.Nil(t, got.Sandbox, "no sandbox when config.Sandbox is zero")
}

func TestBuildBashTool_ExplicitTimeoutHonoured(t *testing.T) {
	got, err := buildBashTool(config.BashConfig{TimeoutSeconds: 5})
	require.NoError(t, err)
	assert.Equal(t, 5*time.Second, got.Timeout)
}

func TestBuildBashTool_NoneSandboxIsEquivalentToUnset(t *testing.T) {
	// Kind "none" means "direct exec" — same as unset.
	got, err := buildBashTool(config.BashConfig{
		Sandbox: config.BashSandboxConfig{Kind: "none"},
	})
	require.NoError(t, err)
	assert.Nil(t, got.Sandbox, "kind=none must not attach a backend")
}

func TestBuildBashTool_UnknownSandboxErrors(t *testing.T) {
	_, err := buildBashTool(config.BashConfig{
		Sandbox: config.BashSandboxConfig{Kind: "not-real"},
	})
	assert.Error(t, err)
}

func TestBuildBashTool_KnownBackendsAttachSandbox(t *testing.T) {
	for _, kind := range []string{"gvisor", "nsjail", "firecracker"} {
		t.Run(kind, func(t *testing.T) {
			got, err := buildBashTool(config.BashConfig{
				Sandbox: config.BashSandboxConfig{Kind: kind},
			})
			require.NoError(t, err)
			require.NotNil(t, got.Sandbox, "kind=%s must attach a backend", kind)
			assert.Equal(t, kind, got.Sandbox.Kind())
		})
	}
}

func TestResolveSandboxPolicy_NoNetworkSafeDefault(t *testing.T) {
	// Unset NoNetwork → safe default: deny egress.
	p := resolveSandboxPolicy(config.BashSandboxConfig{})
	assert.True(t, p.NoNetwork, "unset NoNetwork must default to true (deny egress)")
}

func TestResolveSandboxPolicy_ExplicitNoNetworkFalseHonoured(t *testing.T) {
	// Operator explicitly opts INTO network — respected.
	no := false
	p := resolveSandboxPolicy(config.BashSandboxConfig{NoNetwork: &no})
	assert.False(t, p.NoNetwork, "explicit false must not be overridden")
}

func TestResolveSandboxPolicy_LimitsMapCorrectly(t *testing.T) {
	// Verify the unit translations are honest: MemoryMB is
	// multiplied by 1 MiB; WallclockSeconds is turned into a
	// time.Duration.
	no := true
	p := resolveSandboxPolicy(config.BashSandboxConfig{
		NoNetwork:        &no,
		TmpdirRoot:       "/scratch",
		WallclockSeconds: 30,
		CPUSeconds:       10,
		MemoryMB:         256,
		Readonly:         []string{"/usr"},
		Writable:         []string{"/workspace"},
	})
	assert.Equal(t, "/scratch", p.TmpdirRoot)
	assert.Equal(t, 30*time.Second, p.Wallclock)
	assert.Equal(t, 10, p.CPUSeconds)
	assert.Equal(t, int64(256*1024*1024), p.MemoryBytes)
	assert.Equal(t, []string{"/usr"}, p.Readonly)
	assert.Equal(t, []string{"/workspace"}, p.Writable)
}

func TestBuildBashSandbox_NoneReturnsNilNil(t *testing.T) {
	// (nil, nil) is the "no backend" signal buildBashTool checks
	// for to short-circuit to the direct-exec constructor.
	b, err := buildBashSandbox(config.BashSandboxConfig{})
	require.NoError(t, err)
	assert.Nil(t, b)

	b, err = buildBashSandbox(config.BashSandboxConfig{Kind: "none"})
	require.NoError(t, err)
	assert.Nil(t, b)
}

func TestBuildBashSandbox_KindReachesBackend(t *testing.T) {
	// A non-empty Kind produces a real backend with the resolved
	// policy — asserted via the backend's Kind() label since the
	// internal Policy field isn't exported on every backend.
	b, err := buildBashSandbox(config.BashSandboxConfig{Kind: "gvisor"})
	require.NoError(t, err)
	require.NotNil(t, b)
	assert.Equal(t, "gvisor", b.Kind())

	// Sanity: the resolved policy on the returned backend matches
	// the sandbox package's default disposition (NoNetwork on).
	gv, ok := b.(*sandbox.GVisor)
	require.True(t, ok)
	assert.True(t, gv.Policy.NoNetwork)
}

func TestBuildBashTool_BadKindErrors(t *testing.T) {
	// Config-time error is fatal for the daemon — a mis-typed
	// sandbox kind must surface as an error so the caller can
	// refuse to boot into a permissive fallback.
	_, err := buildBashTool(config.BashConfig{
		Sandbox: config.BashSandboxConfig{Kind: "not-a-backend"},
	})
	assert.Error(t, err)
}
