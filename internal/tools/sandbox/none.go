package sandbox

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
)

// None is the no-isolation backend: it exec's the command directly.
// This matches the pre-sandbox behaviour of the bash tool.
type None struct{}

// Kind returns "none".
func (*None) Kind() string { return "none" }

// Run executes cmd via os/exec with no additional isolation.
func (*None) Run(ctx context.Context, cmd Command) (Result, error) {
	if cmd.Path == "" {
		return Result{}, errors.New("sandbox/none: Command.Path is required")
	}
	c := exec.CommandContext(ctx, cmd.Path, cmd.Args...) //nolint:gosec // caller-vetted
	if len(cmd.Env) > 0 {
		c.Env = cmd.Env
	}
	if cmd.Dir != "" {
		c.Dir = cmd.Dir
	}
	if len(cmd.Stdin) > 0 {
		c.Stdin = bytes.NewReader(cmd.Stdin)
	}
	var buf bytes.Buffer
	c.Stdout = &buf
	c.Stderr = &buf

	err := c.Run()
	res := Result{CombinedOutput: buf.String()}
	if c.ProcessState != nil {
		res.ExitCode = c.ProcessState.ExitCode()
	}
	if err != nil {
		return res, err
	}
	return res, nil
}
