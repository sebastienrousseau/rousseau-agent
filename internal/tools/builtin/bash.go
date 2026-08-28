package builtin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"time"

	"github.com/sebastienrousseau/rousseau-agent/internal/tools"
)

// BashTool executes a shell command via `/bin/sh -c`. It is intentionally
// simple; policy (approval, sandboxing) is enforced by the caller.
type BashTool struct {
	// Timeout caps individual command execution. Zero uses 60s.
	Timeout time.Duration
}

// NewBashTool constructs a BashTool with the given timeout. Zero uses
// the default (60s).
func NewBashTool(timeout time.Duration) *BashTool {
	if timeout == 0 {
		timeout = 60 * time.Second
	}
	return &BashTool{Timeout: timeout}
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

// Execute runs the command with the configured timeout.
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

	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", in.Command)
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

// Compile-time interface satisfaction check.
var _ tools.Tool = (*BashTool)(nil)
