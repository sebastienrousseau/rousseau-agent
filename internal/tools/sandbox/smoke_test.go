package sandbox

import (
	"context"
	"os/exec"
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
