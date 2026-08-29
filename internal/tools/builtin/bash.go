package builtin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"time"

	"github.com/sebastienrousseau/rousseau-agent/internal/tools"
	"github.com/sebastienrousseau/rousseau-agent/internal/tools/sandbox"
)

// BashTool executes a shell command via `/bin/sh -c`. Policy
// (approval) is enforced by the caller; execution isolation is
// enforced by the optional Sandbox field.
//
// When Sandbox is nil (the shipped default), commands run via a
// plain exec.CommandContext — matching the pre-sandbox behaviour so
// existing deployments keep working with no config change.
//
// When Sandbox is set, commands run inside the chosen backend
// (gvisor / nsjail / firecracker; None is a valid choice too). The
// caller controls the Policy through the backend constructor; the
// tool is a passive consumer.
type BashTool struct {
	// Timeout caps individual command execution. Zero uses 60s.
	Timeout time.Duration
	// Sandbox is the execution backend. Nil uses direct exec
	// (pre-sandbox behaviour). Set via [NewBashToolWithSandbox] or
	// by assigning after construction.
	Sandbox sandbox.Backend
}

// NewBashTool constructs a BashTool with the given timeout and no
// sandbox (direct exec). Zero timeout uses 60s.
//
// New callers that want sandboxing should use
// [NewBashToolWithSandbox] — this constructor is preserved so
// existing wire-ups keep compiling with unchanged semantics.
func NewBashTool(timeout time.Duration) *BashTool {
	if timeout == 0 {
		timeout = 60 * time.Second
	}
	return &BashTool{Timeout: timeout}
}

// NewBashToolWithSandbox constructs a BashTool that routes every
// command through backend. Zero timeout uses 60s; nil backend
// behaves identically to [NewBashTool].
func NewBashToolWithSandbox(timeout time.Duration, backend sandbox.Backend) *BashTool {
	t := NewBashTool(timeout)
	t.Sandbox = backend
	return t
}

// Name returns the tool identifier.
func (*BashTool) Name() string { return "bash" }

// Description returns the model-facing description.
func (*BashTool) Description() string {
	return "Execute a shell command via `/bin/sh -c`. Returns combined stdout+stderr with exit status."
}

// InputSchema returns the tool's input JSON Schema.
func (*BashTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"command": map[string]any{
				"type":        "string",
				"description": "The shell command to execute.",
			},
		},
		"required": []string{"command"},
	}
}

type bashInput struct {
	Command string `json:"command"`
}

// Execute runs the command with the configured timeout, through the
// configured sandbox when one is set.
func (t *BashTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var in bashInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return "", fmt.Errorf("bash: parse input: %w", err)
	}
	if in.Command == "" {
		return "", fmt.Errorf("bash: command is required")
	}

	ctx, cancel := context.WithTimeout(ctx, t.Timeout)
	defer cancel()

	if t.Sandbox != nil {
		return t.runSandboxed(ctx, in.Command)
	}
	return t.runDirect(ctx, in.Command)
}

// runDirect is the pre-sandbox code path: exec.CommandContext with
// combined stdout+stderr. Preserved so a caller who did not opt into
// sandboxing gets exactly today's behaviour, byte for byte.
func (t *BashTool) runDirect(ctx context.Context, command string) (string, error) {
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", command)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf

	err := cmd.Run()
	out := buf.String()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return out, fmt.Errorf("bash: timed out after %s", t.Timeout)
		}
		return out, fmt.Errorf("bash: %w", err)
	}
	return out, nil
}

// runSandboxed delegates to the configured backend. Backend-specific
// errors surface transparently; the deadline handling mirrors the
// direct path so the model sees a consistent error shape.
func (t *BashTool) runSandboxed(ctx context.Context, command string) (string, error) {
	res, err := t.Sandbox.Run(ctx, sandbox.Command{
		Path: "/bin/sh",
		Args: []string{"-c", command},
	})
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return res.CombinedOutput, fmt.Errorf("bash: timed out after %s", t.Timeout)
		}
		return res.CombinedOutput, fmt.Errorf("bash: %w", err)
	}
	return res.CombinedOutput, nil
}

// Compile-time interface satisfaction check.
var _ tools.Tool = (*BashTool)(nil)
