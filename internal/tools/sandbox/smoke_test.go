package sandbox

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The argv builders are unit-tested, but only a real jail proves the
// flags are accepted and /bin/sh exists inside it. These run when the
// binary is on $PATH (the agent-builder image, a developer box with
// nsjail installed) and skip otherwise, so CI on a plain runner stays
// green without pretending the sandbox was exercised.
func TestSmoke_NSJailRunsShell(t *testing.T) {
	if _, err := exec.LookPath("nsjail"); err != nil {
		t.Skip("nsjail not on PATH")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := (&NSJail{Policy: DefaultPolicy()}).Run(ctx, Command{
		Path: "/bin/sh",
		Args: []string{"-c", "echo ok; ls / | head -20"},
		Env:  []string{"PATH=/usr/bin:/bin"},
	})
	require.NoError(t, err, res.CombinedOutput)
	assert.Contains(t, res.CombinedOutput, "ok")
	assert.Equal(t, 0, res.ExitCode)
	// The host's home directories must not be visible.
	assert.NotContains(t, strings.ToLower(res.CombinedOutput), "home")
}

func TestSmoke_GVisorRunsShell(t *testing.T) {
	if _, err := exec.LookPath("runsc"); err != nil {
		t.Skip("runsc not on PATH")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	res, err := (&GVisor{Policy: DefaultPolicy()}).Run(ctx, Command{
		Path: "/bin/sh",
		Args: []string{"-c", "echo ok"},
		Env:  []string{"PATH=/usr/bin:/bin"},
	})
	require.NoError(t, err, res.CombinedOutput)
	assert.Contains(t, res.CombinedOutput, "ok")
}

// M-11: a file under $HOME must not be readable inside the gVisor
// sandbox, and a write must not reach the host.
func TestSmoke_GVisorHidesHome(t *testing.T) {
	if _, err := exec.LookPath("runsc"); err != nil {
		t.Skip("runsc not on PATH")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	secret := filepath.Join(home, "secret")
	require.NoError(t, os.WriteFile(secret, []byte("s3cr3t-marker"), 0o600))
	ws := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	res, err := (&GVisor{Policy: Policy{NoNetwork: true, Writable: []string{ws}}}).Run(ctx, Command{
		Path: "/bin/sh",
		Args: []string{"-c", "cat " + secret + "; echo x > " + ws + "/written"},
		Env:  []string{"PATH=/usr/bin:/bin", "HOME=" + home},
	})
	require.NoError(t, err, res.CombinedOutput)
	assert.NotContains(t, res.CombinedOutput, "s3cr3t-marker")
	assert.NoFileExists(t, filepath.Join(ws, "written"), "the overlay keeps writes off the host")
}
