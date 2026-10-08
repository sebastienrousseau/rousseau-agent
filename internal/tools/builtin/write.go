package builtin

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/sebastienrousseau/rousseau-agent/internal/tools"
	"github.com/sebastienrousseau/rousseau-agent/internal/tools/fsguard"
)

// WriteTool writes a UTF-8 text file to disk, creating parent directories
// as needed. It is intentionally a full-file overwrite; incremental edits
// go through EditTool.
type WriteTool struct {
	// Guard decides which paths may be written. Nil uses
	// fsguard.Default (deny list, no workspace root).
	Guard *fsguard.Guard
}

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
	path, err := resolveWritePath(t.Guard, "write", in.Path)
	if err != nil {
		return "", err
	}
	if err := guardOrDefault(t.Guard).WriteFile(path, []byte(in.Content), 0o644); err != nil {
		return "", fmt.Errorf("write: %w", err)
	}
	return fmt.Sprintf("wrote %d bytes to %s", len(in.Content), in.Path), nil
}

// Compile-time interface satisfaction check.
var _ tools.Tool = (*WriteTool)(nil)
