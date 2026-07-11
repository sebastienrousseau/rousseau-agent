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

// EditTool performs an exact-string replacement inside a file.
// old_string MUST be unique in the file — this is a deliberate constraint
// borrowed from Claude Code's Edit tool. It prevents accidental
// mass-replacement and forces the model to disambiguate.
type EditTool struct{}

// NewEditTool constructs an EditTool.
func NewEditTool() *EditTool { return &EditTool{} }

// Name returns the tool identifier.
func (*EditTool) Name() string { return "edit" }

// Description returns the model-facing description.
func (*EditTool) Description() string {
	return "Replace exactly one occurrence of old_string with new_string in a file. old_string must be unique in the file; if it appears zero or multiple times the edit fails. Preserve indentation exactly."
}

// InputSchema returns the tool's input JSON Schema.
func (*EditTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Absolute filesystem path to the file to edit.",
			},
			"old_string": map[string]any{
				"type":        "string",
				"description": "Exact text to find. Must be unique in the file.",
			},
			"new_string": map[string]any{
				"type":        "string",
				"description": "Text to replace old_string with.",
			},
		},
		"required": []string{"path", "old_string", "new_string"},
	}
}

type editInput struct {
	Path      string `json:"path"`
	OldString string `json:"old_string"`
	NewString string `json:"new_string"`
}

// Execute runs the tool.
func (t *EditTool) Execute(_ context.Context, raw json.RawMessage) (string, error) {
	var in editInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return "", fmt.Errorf("edit: parse input: %w", err)
	}
	if in.Path == "" {
		return "", fmt.Errorf("edit: path is required")
	}
	if !filepath.IsAbs(in.Path) {
		return "", fmt.Errorf("edit: path must be absolute, got %q", in.Path)
	}
	if in.OldString == "" {
		return "", fmt.Errorf("edit: old_string is required")
	}
	if in.OldString == in.NewString {
		return "", fmt.Errorf("edit: old_string and new_string are identical")
	}

	b, err := os.ReadFile(in.Path)
	if err != nil {
		return "", fmt.Errorf("edit: read: %w", err)
	}
	original := string(b)

	count := strings.Count(original, in.OldString)
	switch count {
	case 0:
		return "", fmt.Errorf("edit: old_string not found in %s", in.Path)
	case 1:
		// ok
	default:
		return "", fmt.Errorf("edit: old_string is not unique in %s (found %d occurrences); provide more surrounding context", in.Path, count)
	}

	updated := strings.Replace(original, in.OldString, in.NewString, 1)
	if err := os.WriteFile(in.Path, []byte(updated), 0o644); err != nil {
		return "", fmt.Errorf("edit: write: %w", err)
	}
	return fmt.Sprintf("edited %s (1 replacement)", in.Path), nil
}

// Compile-time interface satisfaction check.
var _ tools.Tool = (*EditTool)(nil)
