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
	t.Setenv("HOME", t.TempDir())
	bin := fakeRuntime(t, "runsc")
	g := &sandbox.GVisor{Binary: bin} // zero Policy — no NoNetwork
	res, err := g.Run(context.Background(), sandbox.Command{
		Path: "/bin/sh",
		Args: []string{"-c", "echo hi"},
	})
	require.NoError(t, err)
	got := argvOf(res.CombinedOutput)
	// Global flags: --rootless, then --root=<state dir>; NoNetwork was
	// zero so no --network=none.
	assert.Equal(t, "--rootless", got[0])
	assert.True(t, strings.HasPrefix(got[1], "--root="), "second flag should be --root=<state dir>")
	assert.Equal(t, "do", got[2])
	// The `do` flags: a forced overlay, a private root, cwd inside it.
	sep := indexOf(got, "--")
	require.Positive(t, sep)
	doFlags := got[3:sep]
	assert.Contains(t, doFlags, "-force-overlay=true")
	assert.Contains(t, doFlags, "-cwd=/")
	assert.Equal(t, []string{"--", "/bin/sh", "-c", "echo hi"}, got[sep:])
}

func indexOf(xs []string, want string) int {
	for i, x := range xs {
		if x == want {
			return i
		}
	}
	return -1
}

// gvisorLayout runs a fake runsc that echoes its argv and lists the
// rootfs directory passed via `do -root=`, so a test can assert what
// the sandboxed process would see without a real runsc.
func gvisorLayout(t *testing.T, pol sandbox.Policy) (argv, layout []string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX-only fixture")
	}
	path := filepath.Join(t.TempDir(), "runsc")
	const script = `#!/bin/sh
for a in "$@"; do
  printf 'argv:%s\n' "$a"
  case "$a" in
    -root=*) (cd "${a#-root=}" && find . -type l | sed 's/^/link:/'; find . -type d | sed 's/^/dir:/'; find . -type f | sed 's/^/file:/') ;;
  esac
done
`
	require.NoError(t, os.WriteFile(path, []byte(script), 0o755)) //nolint:gosec // deliberately executable test fixture
	res, err := (&sandbox.GVisor{Binary: path, Policy: pol}).Run(context.Background(), sandbox.Command{Path: "/bin/true"})
	require.NoError(t, err, res.CombinedOutput)
	for _, line := range strings.Split(strings.TrimSpace(res.CombinedOutput), "\n") {
		if !strings.HasPrefix(line, "argv:") {
			layout = append(layout, line)
		}
	}
	return argvOf(res.CombinedOutput), layout
}

func doRoot(t *testing.T, argv []string) string {
	t.Helper()
	for _, a := range argv {
		if after, ok := strings.CutPrefix(a, "-root="); ok {
			return after
		}
	}
	t.Fatalf("no `do -root=` in argv %q", argv)
	return ""
}

// M-11: `runsc do` without -root runs against the host's "/", so a
// "sandboxed" bash could read ~/.ssh and the daemon config. The do
// root must be a private directory holding only the mounted paths.
func TestGVisor_DoRootIsPrivateNotHostSlash(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	ws := t.TempDir()
	argv, layout := gvisorLayout(t, sandbox.Policy{NoNetwork: true, Writable: []string{ws}})

	root := doRoot(t, argv)
	assert.NotEqual(t, "/", filepath.Clean(root), "the sandbox root must not be the host /")
	assert.Contains(t, argv, "-volume="+ws+":"+ws, "the workspace is bind-mounted")
	assert.Contains(t, layout, "dir:."+ws, "the workspace mount point exists in the root")
	for _, a := range argv {
		if src, ok := strings.CutPrefix(a, "-volume="); ok {
			src, _, _ = strings.Cut(src, ":")
			assert.False(t, src == "/" || strings.HasPrefix(home+"/", src+"/"),
				"volume %q exposes $HOME", src)
		}
	}
	for _, l := range layout {
		assert.NotContains(t, l, home, "nothing under $HOME may appear in the root")
	}
}

// A symlinked system dir (merged /usr: /bin -> usr/bin) is recreated
// as the same link inside the root instead of being bind-mounted, so
// the link resolves inside the sandbox.
func TestGVisor_SymlinkedReadonlyBecomesLinkInRoot(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	target := filepath.Join(dir, "real")
	require.NoError(t, os.Mkdir(target, 0o755))
	link := filepath.Join(dir, "link")
	require.NoError(t, os.Symlink("real", link))

	argv, layout := gvisorLayout(t, sandbox.Policy{Readonly: []string{target, link}})
	assert.Contains(t, argv, "-volume="+target+":"+target)
	assert.NotContains(t, argv, "-volume="+link+":"+link)
	assert.Contains(t, layout, "link:."+link)
}

// A mount that is $HOME, an ancestor of it, or "/" would put the
// secrets the sandbox exists to hide back inside it; Run refuses.
func TestGVisor_RefusesMountExposingHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	bin := fakeRuntime(t, "runsc")
	for _, p := range []sandbox.Policy{
		{Writable: []string{home}},
		{Writable: []string{filepath.Dir(home)}},
		{Readonly: []string{"/"}},
	} {
		_, err := (&sandbox.GVisor{Binary: bin, Policy: p}).Run(context.Background(), sandbox.Command{Path: "/bin/true"})
		require.Error(t, err, "%+v", p)
		assert.Contains(t, err.Error(), "HOME")
	}
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
	// Always-on prefix. nsjail's --disable_clone_* switches take no
	// value; the old "=false" spelling was rejected by its parser.
	assert.Equal(t, []string{"--quiet", "--mode", "o", "--disable_clone_newuser", "--keep_env"}, got[:5])
	// Zero Policy allows network, so the network namespace is disabled.
	assert.Equal(t, "--disable_clone_newnet", got[5])
	// Scratch dir is the cwd and a writable bindmount
	// (Policy.TmpdirRoot empty → os.TempDir()):
	require.Equal(t, "--cwd", got[6])
	assert.Contains(t, got[7], "rousseau-nsjail-", "scratch cwd uses the per-invocation tmpdir prefix")
	require.Equal(t, "--bindmount", got[8])
	assert.Contains(t, got[9], "rousseau-nsjail-")
	// With no Readonly set the minimal root is mounted so /bin/sh
	// exists inside the jail.
	joined := strings.Join(got, " ")
	assert.Contains(t, joined, "--bindmount_ro /usr:/usr")
	assert.Contains(t, joined, "--bindmount_ro /etc/resolv.conf:/etc/resolv.conf", "network allowed, so DNS config is mounted")
	// Terminated by --, then the wrapped command:
	assert.Equal(t, []string{"--", "/bin/sh", "-c", "echo hi"}, got[len(got)-4:])
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
	assert.NotContains(t, joined, "--disable_clone_newnet", "NoNetwork keeps nsjail's default fresh netns")
	assert.Contains(t, joined, "--disable_proc")
	assert.NotContains(t, joined, "resolv.conf", "explicit Readonly replaces the default root")
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
