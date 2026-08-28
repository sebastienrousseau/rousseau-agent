// Package builtin re-exports the reference tools (read, write, edit,
// grep, bash) so external modules can register them into their own
// [tools.Registry] without importing /internal.
package builtin

import (
	"time"

	"github.com/sebastienrousseau/rousseau-agent/internal/tools/builtin"
)

// NewReadTool constructs a read tool.
func NewReadTool() *builtin.ReadTool { return builtin.NewReadTool() }

// NewWriteTool constructs a write tool.
func NewWriteTool() *builtin.WriteTool { return builtin.NewWriteTool() }

// NewEditTool constructs an edit tool.
func NewEditTool() *builtin.EditTool { return builtin.NewEditTool() }

// NewGrepTool constructs a grep tool with match + file-size caps.
// Zero values use built-in defaults.
func NewGrepTool(maxMatches int, maxFileBytes int64) *builtin.GrepTool {
	return builtin.NewGrepTool(maxMatches, maxFileBytes)
}

// NewBashTool constructs a bash tool with the given per-command
// timeout. Zero falls back to 60s.
func NewBashTool(timeout time.Duration) *builtin.BashTool {
	return builtin.NewBashTool(timeout)
}
