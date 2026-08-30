package sandbox_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/tools/sandbox"
)

// fakeRuntime writes a /bin/sh stand-in that echoes its argv, one
// element per line, plus $PWD and stdin when asked. Backends that
// "shell out" are asserted on the argv they build rather than by
// spawning a real runsc / nsjail.
func fakeRuntime(t *testing.T, name string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX-only fixture")
	}
	path := filepath.Join(t.TempDir(), name)
	const script = "#!/bin/sh\nfor a in \"$@\"; do printf 'argv:%s\\n' \"$a\"; done\n"
	require.NoError(t, os.WriteFile(path, []byte(script), 0o755)) //nolint:gosec // deliberately executable test fixture
	return path
}

func argvOf(out string) []string {
	var got []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if after, ok := strings.CutPrefix(line, "argv:"); ok {
			got = append(got, after)
		}
	}
	return got
}

func TestNone_PassesEnvDirAndStdin(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX-only fixture")
	}
	dir := t.TempDir()
	s := &sandbox.None{}
	res, err := s.Run(context.Background(), sandbox.Command{
		Path:  "/bin/sh",
		Args:  []string{"-c", `printf '%s|%s|' "$MARKER" "$PWD"; cat`},
		Env:   []string{"MARKER=from-env"},
		Dir:   dir,
		Stdin: []byte("from-stdin"),
	})
	require.NoError(t, err)

	parts := strings.SplitN(res.CombinedOutput, "|", 3)
	require.Len(t, parts, 3)
	assert.Equal(t, "from-env", parts[0], "Command.Env must reach the subprocess")
	// macOS resolves TMPDIR through /private; compare the resolved form.
	wantDir, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)
	gotDir, err := filepath.EvalSymlinks(parts[1])
	require.NoError(t, err)
	assert.Equal(t, wantDir, gotDir, "Command.Dir must be the subprocess cwd")
	assert.Equal(t, "from-stdin", parts[2], "Command.Stdin must be fed to the subprocess")
	assert.Equal(t, 0, res.ExitCode)
}

func TestNone_EmptyEnvInheritsParent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX-only fixture")
	}
	t.Setenv("ROUSSEAU_SANDBOX_MARKER", "inherited")
	s := &sandbox.None{}
	res, err := s.Run(context.Background(), sandbox.Command{
		Path: "/bin/sh",
		Args: []string{"-c", `printf '%s' "$ROUSSEAU_SANDBOX_MARKER"`},
	})
	require.NoError(t, err)
	assert.Equal(t, "inherited", res.CombinedOutput)
}

func TestGVisor_BuildsRunscDoArgv(t *testing.T) {
	bin := fakeRuntime(t, "runsc")
	g := &sandbox.GVisor{Binary: bin} // zero Policy — no NoNetwork
	res, err := g.Run(context.Background(), sandbox.Command{
		Path: "/bin/sh",
		Args: []string{"-c", "echo hi"},
	})
	require.NoError(t, err)
	got := argvOf(res.CombinedOutput)
	// --rootless is always on; --root=<tmpdir> is always on (per-
	// invocation tmpdir); NoNetwork was zero so no --network=none.
	assert.Equal(t, "--rootless", got[0])
	assert.True(t, strings.HasPrefix(got[1], "--root="), "second flag should be --root=<tmpdir>")
	assert.Equal(t, []string{"do", "--", "/bin/sh", "-c", "echo hi"}, got[2:])
}

func TestGVisor_DefaultPolicyFiresNoNetwork(t *testing.T) {
	// newGVisor() (invoked via sandbox.New("gvisor")) uses
	// DefaultPolicy which flips NoNetwork on. This test proves the
	// safe default rides all the way through to the argv.
	bin := fakeRuntime(t, "runsc")
	g := &sandbox.GVisor{Binary: bin, Policy: sandbox.DefaultPolicy()}
	res, err := g.Run(context.Background(), sandbox.Command{Path: "/bin/true"})
	require.NoError(t, err)
	got := argvOf(res.CombinedOutput)
	assert.Contains(t, got, "--network=none",
		"DefaultPolicy → NoNetwork → --network=none must appear in the argv")
}

func TestGVisor_DefaultsToRunscOnPath(t *testing.T) {
	// Empty Binary resolves "runsc" via $PATH. A PATH containing only
	// an empty dir guarantees a miss without touching the real host.
	t.Setenv("PATH", t.TempDir())
	g := &sandbox.GVisor{}
	_, err := g.Run(context.Background(), sandbox.Command{Path: "/bin/true"})
	assert.ErrorIs(t, err, sandbox.ErrUnavailable)
}

func TestNSJail_BuildsBaselineArgv(t *testing.T) {
	// Zero Policy still fires the three always-on flags. Scratch
	// bindmount is always present (a per-invocation tmpdir).
	bin := fakeRuntime(t, "nsjail")
	n := &sandbox.NSJail{Binary: bin}
	res, err := n.Run(context.Background(), sandbox.Command{
		Path: "/bin/sh",
		Args: []string{"-c", "echo hi"},
	})
	require.NoError(t, err)
	got := argvOf(res.CombinedOutput)
	// Always-on prefix:
	assert.Equal(t, []string{"--quiet", "--mode", "o", "--disable_clone_newuser=false"}, got[:4])
	// Scratch bindmount (Policy.TmpdirRoot empty → os.TempDir()):
	require.Equal(t, "--bindmount", got[4])
	assert.Contains(t, got[5], "rousseau-nsjail-", "scratch bindmount uses the per-invocation tmpdir prefix")
	// Terminated by --, then the wrapped command:
	assert.Equal(t, []string{"--", "/bin/sh", "-c", "echo hi"}, got[6:])
}

func TestNSJail_PolicyPropagatesLimitsAndBindmounts(t *testing.T) {
	// A fully-populated Policy exercises every branch in nsjailArgs.
	bin := fakeRuntime(t, "nsjail")
	tmpRoot := t.TempDir() // pin the tmpdir root so the test can predict the argv
	n := &sandbox.NSJail{
		Binary: bin,
		Policy: sandbox.Policy{
			NoNetwork:   true,
			TmpdirRoot:  tmpRoot,
			Wallclock:   30 * time.Second,
			CPUSeconds:  10,
			MemoryBytes: 256 * 1024 * 1024, // 256 MiB
			Readonly:    []string{"/usr", "/lib"},
			Writable:    []string{"/workspace"},
		},
	}
	res, err := n.Run(context.Background(), sandbox.Command{Path: "/bin/true"})
	require.NoError(t, err)
	got := argvOf(res.CombinedOutput)
	// Presence-only assertions — order stability is nice but not
	// contractually load-bearing on individual flags.
	joined := strings.Join(got, " ")
	assert.Contains(t, joined, "--disable_clone_newnet=false") // NoNetwork
	assert.Contains(t, joined, "--disable_proc")
	assert.Contains(t, joined, "--time_limit 30")       // Wallclock 30s
	assert.Contains(t, joined, "--rlimit_cpu 10")       // CPUSeconds 10
	assert.Contains(t, joined, "--rlimit_as 256")       // MemoryBytes 256 MiB
	assert.Contains(t, joined, "/usr:/usr")             // Readonly[0]
	assert.Contains(t, joined, "/lib:/lib")             // Readonly[1]
	assert.Contains(t, joined, "/workspace:/workspace") // Writable[0]
	assert.Contains(t, joined, "rousseau-nsjail-")      // scratch bindmount
	// Every -- (there's one after the argv terminator; each --flag
	// counts too) — verify the terminator sits before the wrapped cmd.
	assert.Contains(t, got, "/bin/true")
}

func TestNSJail_DefaultsToNsjailOnPath(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	n := &sandbox.NSJail{}
	_, err := n.Run(context.Background(), sandbox.Command{Path: "/bin/true"})
	assert.ErrorIs(t, err, sandbox.ErrUnavailable)
}

// TestWrappedBackends_ForwardEnvDirStdin asserts the scaffolded
// backends hand the caller's environment/cwd/stdin through to the
// wrapped process rather than dropping them.
func TestWrappedBackends_ForwardEnvDirStdin(t *testing.T) {
	for _, tc := range []struct {
		name    string
		binName string
		backend func(bin string) sandbox.Backend
	}{
		{"gvisor", "runsc", func(bin string) sandbox.Backend { return &sandbox.GVisor{Binary: bin} }},
		{"nsjail", "nsjail", func(bin string) sandbox.Backend { return &sandbox.NSJail{Binary: bin} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if runtime.GOOS == "windows" {
				t.Skip("POSIX-only fixture")
			}
			dir := t.TempDir()
			path := filepath.Join(t.TempDir(), tc.binName)
			const script = "#!/bin/sh\nprintf '%s|%s|' \"$MARKER\" \"$PWD\"\ncat\n"
			require.NoError(t, os.WriteFile(path, []byte(script), 0o755)) //nolint:gosec // deliberately executable test fixture

			res, err := tc.backend(path).Run(context.Background(), sandbox.Command{
				Path:  "/bin/true",
				Env:   []string{"MARKER=wrapped"},
				Dir:   dir,
				Stdin: []byte("piped"),
			})
			require.NoError(t, err)
			parts := strings.SplitN(res.CombinedOutput, "|", 3)
			require.Len(t, parts, 3)
			assert.Equal(t, "wrapped", parts[0])
			assert.NotEmpty(t, parts[1])
			assert.Equal(t, "piped", parts[2])
		})
	}
}

func TestGVisor_TmpdirFailureSurfaces(t *testing.T) {
	// TmpdirRoot pointing at a non-writable path makes
	// os.MkdirTemp fail — surface as an error rather than crash
	// or fall through to the wrapped exec.
	bin := fakeRuntime(t, "runsc")
	g := &sandbox.GVisor{Binary: bin, Policy: sandbox.Policy{TmpdirRoot: "/proc/nonwriteable/subdir/that/does/not/exist"}}
	_, err := g.Run(context.Background(), sandbox.Command{Path: "/bin/true"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "tmpdir")
}

func TestNSJail_TmpdirFailureSurfaces(t *testing.T) {
	bin := fakeRuntime(t, "nsjail")
	n := &sandbox.NSJail{Binary: bin, Policy: sandbox.Policy{TmpdirRoot: "/proc/nonwriteable/subdir/that/does/not/exist"}}
	_, err := n.Run(context.Background(), sandbox.Command{Path: "/bin/true"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "tmpdir")
}

func TestNSJail_SubMiBMemoryClampedUpToOne(t *testing.T) {
	// MemoryBytes < 1 MiB is a real footgun — the operator likely
	// meant KB, not bytes. Rather than pass a rlimit_as of 0 (which
	// nsjail interprets as "no limit"), clamp UP to 1 MiB so the
	// operator's intent (a small limit) is preserved instead of
	// silently becoming "no limit".
	bin := fakeRuntime(t, "nsjail")
	n := &sandbox.NSJail{Binary: bin, Policy: sandbox.Policy{MemoryBytes: 1024}} // 1 KiB
	res, err := n.Run(context.Background(), sandbox.Command{Path: "/bin/true"})
	require.NoError(t, err)
	joined := strings.Join(argvOf(res.CombinedOutput), " ")
	assert.Contains(t, joined, "--rlimit_as 1")
}
