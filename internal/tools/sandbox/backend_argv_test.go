package sandbox_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"context"

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
	g := &sandbox.GVisor{Binary: bin}
	res, err := g.Run(context.Background(), sandbox.Command{
		Path: "/bin/sh",
		Args: []string{"-c", "echo hi"},
	})
	require.NoError(t, err)
	assert.Equal(t,
		[]string{"do", "/bin/sh", "-c", "echo hi"},
		argvOf(res.CombinedOutput),
		"gVisor must prepend `do` then the original argv")
}

func TestGVisor_DefaultsToRunscOnPath(t *testing.T) {
	// Empty Binary resolves "runsc" via $PATH. A PATH containing only
	// an empty dir guarantees a miss without touching the real host.
	t.Setenv("PATH", t.TempDir())
	g := &sandbox.GVisor{}
	_, err := g.Run(context.Background(), sandbox.Command{Path: "/bin/true"})
	assert.ErrorIs(t, err, sandbox.ErrUnavailable)
}

func TestNSJail_BuildsQuietArgv(t *testing.T) {
	bin := fakeRuntime(t, "nsjail")
	n := &sandbox.NSJail{Binary: bin}
	res, err := n.Run(context.Background(), sandbox.Command{
		Path: "/bin/sh",
		Args: []string{"-c", "echo hi"},
	})
	require.NoError(t, err)
	assert.Equal(t,
		[]string{"--quiet", "--", "/bin/sh", "-c", "echo hi"},
		argvOf(res.CombinedOutput),
		"nsjail must pass --quiet and terminate its own flags with --")
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
