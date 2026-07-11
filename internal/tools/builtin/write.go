package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/sebastienrousseau/rousseau-agent/internal/tools"
)

// WriteTool writes a UTF-8 text file to disk, creating parent directories
// as needed. It is intentionally a full-file overwrite; incremental edits
// go through EditTool.
type WriteTool struct{}

// NewWriteTool constructs a WriteTool.
func NewWriteTool() *WriteTool { return &WriteTool{} }

// Name returns the tool identifier.
func (*WriteTool) Name() string { return "write" }

// Description returns the model-facing description.
func (*WriteTool) Description() string {
	return "Write UTF-8 text to a file, replacing existing contents. Creates parent directories as needed. Input: absolute path + content."
}

// InputSchema returns the tool's input JSON Schema.
func (*WriteTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Absolute filesystem path to write.",
			},
			"content": map[string]any{
				"type":        "string",
				"description": "The complete file contents to write.",
			},
		},
		"required": []string{"path", "content"},
	}
}

type writeInput struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// Execute runs the tool.
func (t *WriteTool) Execute(_ context.Context, raw json.RawMessage) (string, error) {
	var in writeInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return "", fmt.Errorf("write: parse input: %w", err)
	}
	if in.Path == "" {
		return "", fmt.Errorf("write: path is required")
	}
	if !filepath.IsAbs(in.Path) {
		return "", fmt.Errorf("write: path must be absolute, got %q", in.Path)
	}
	if err := os.MkdirAll(filepath.Dir(in.Path), 0o755); err != nil {
		return "", fmt.Errorf("write: mkdir: %w", err)
	}
	if err := os.WriteFile(in.Path, []byte(in.Content), 0o644); err != nil {
		return "", fmt.Errorf("write: %w", err)
	}
	return fmt.Sprintf("wrote %d bytes to %s", len(in.Content), in.Path), nil
}

// Compile-time interface satisfaction check.
var _ tools.Tool = (*WriteTool)(nil)
