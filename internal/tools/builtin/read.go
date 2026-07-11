// Package builtin ships the reference tools bundled with rousseau-agent.
package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sebastienrousseau/rousseau-agent/internal/tools"
)

// ReadTool reads a UTF-8 text file from the local filesystem.
type ReadTool struct{}

// NewReadTool constructs a ReadTool.
func NewReadTool() *ReadTool { return &ReadTool{} }

// Name returns the tool identifier.
func (*ReadTool) Name() string { return "read" }

// Description returns the model-facing description.
func (*ReadTool) Description() string {
	return "Read the contents of a UTF-8 text file. Input: absolute path. Returns file contents or an error."
}

// InputSchema returns the tool's input JSON Schema.
func (*ReadTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Absolute filesystem path to the file to read.",
			},
		},
		"required": []string{"path"},
	}
}

type readInput struct {
	Path string `json:"path"`
}

// Execute runs the tool.
func (t *ReadTool) Execute(_ context.Context, raw json.RawMessage) (string, error) {
	var in readInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return "", fmt.Errorf("read: parse input: %w", err)
	}
	if in.Path == "" {
		return "", fmt.Errorf("read: path is required")
	}
	if !filepath.IsAbs(in.Path) {
		return "", fmt.Errorf("read: path must be absolute, got %q", in.Path)
	}
	b, err := os.ReadFile(in.Path)
	if err != nil {
		return "", fmt.Errorf("read: %w", err)
	}
	if !isLikelyText(b) {
		return "", fmt.Errorf("read: %s does not look like UTF-8 text", in.Path)
	}
	return string(b), nil
}

// Compile-time interface satisfaction check.
var _ tools.Tool = (*ReadTool)(nil)

func isLikelyText(b []byte) bool {
	const sniff = 512
	if len(b) > sniff {
		b = b[:sniff]
	}
	if strings.ContainsRune(string(b), '\x00') {
		return false
	}
	return true
}
