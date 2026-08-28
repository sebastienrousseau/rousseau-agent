package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSummarizeToolInput(t *testing.T) {
	tests := []struct {
		name string
		tool string
		in   string
		want string
	}{
		{"empty input", "Read", ``, ""},
		{"non-JSON input", "Read", `not-json`, ""},
		{"empty object", "Read", `{}`, ""},

		{"Read shows file_path", "Read", `{"file_path": "internal/foo.go"}`, "internal/foo.go"},
		{"Write shows file_path", "write", `{"file_path": "bar.go", "content": "…"}`, "bar.go"},
		{"Edit shows file_path", "Edit", `{"file_path": "baz.go", "old_string": "a", "new_string": "b"}`, "baz.go"},
		{"NotebookEdit prefers notebook_path", "NotebookEdit", `{"notebook_path": "nb.ipynb"}`, "nb.ipynb"},

		{"Bash shows first line of command", "Bash", `{"command": "go test ./...\ngo vet ./..."}`, "go test ./..."},
		{"Bash trims surrounding whitespace", "Bash", `{"command": "   ls   "}`, "ls"},

		{"Grep prefers pattern", "Grep", `{"pattern": "handleMessage", "path": "internal/"}`, "handleMessage"},
		{"Grep falls back to path", "Grep", `{"path": "internal/"}`, "internal/"},

		{"Glob shows pattern", "Glob", `{"pattern": "**/*.go"}`, "**/*.go"},

		{"WebFetch shows url", "WebFetch", `{"url": "https://example.com/x"}`, "https://example.com/x"},
		{"WebSearch shows query", "WebSearch", `{"query": "claude cli"}`, "claude cli"},

		{"Task shows description", "Task", `{"description": "audit", "prompt": "long…"}`, "audit"},
		{"Task falls back to subagent_type", "Task", `{"subagent_type": "Explore"}`, "Explore"},

		{"TodoWrite renders item count singular", "TodoWrite", `{"todos": [{"a":1}]}`, "1 todo"},
		{"TodoWrite renders item count plural", "TodoWrite", `{"todos": [{"a":1},{"b":2},{"c":3}]}`, "3 todos"},
		{"TodoWrite empty list yields nothing", "TodoWrite", `{"todos": []}`, ""},

		{"unknown tool falls back to first common field", "MysteryTool", `{"path": "/tmp/x"}`, "/tmp/x"},
		{"unknown tool with no common field yields empty", "MysteryTool", `{"nonsense": true}`, ""},

		{"long string is truncated with …", "Read", `{"file_path": "` + strings.Repeat("x", 60) + `"}`, strings.Repeat("x", 47) + "…"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := summarizeToolInput(tc.tool, json.RawMessage(tc.in))
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestSummarizeToolInput_CaseInsensitiveToolName(t *testing.T) {
	// Providers vary: "Read" vs "read" vs "READ". The summariser
	// must not care.
	for _, name := range []string{"Read", "read", "READ", "ReAd"} {
		got := summarizeToolInput(name, json.RawMessage(`{"file_path": "x.go"}`))
		assert.Equal(t, "x.go", got, "tool name %q", name)
	}
}

func TestFirstLine(t *testing.T) {
	assert.Equal(t, "one", firstLine("one\ntwo"))
	assert.Equal(t, "one", firstLine("one\r\ntwo"))
	assert.Equal(t, "one", firstLine("one"))
	assert.Equal(t, "", firstLine(""))
	assert.Equal(t, "hello", firstLine("  hello  \n"))
}

func TestTrimSummary(t *testing.T) {
	assert.Equal(t, "", trimSummary(""))
	assert.Equal(t, "short", trimSummary("short"))

	long := strings.Repeat("é", 60) // rune-safe: don't split
	got := trimSummary(long)
	assert.Equal(t, 48, len([]rune(got)))
	assert.True(t, strings.HasSuffix(got, "…"))
}
