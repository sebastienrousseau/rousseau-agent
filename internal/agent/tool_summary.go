package agent

import (
	"encoding/json"
	"strconv"
	"strings"
)

// summarizeToolInput extracts a short, human-friendly hint from a
// tool's JSON input for the progress feed — the "foo.go" in
// "● Read foo.go", the "go test" in "● Bash go test".
//
// The name is deliberately case-insensitive and lightly typed:
// individual tool packages don't need to know about the progress
// pipeline, and third-party tools automatically get a best-effort
// summary from a small set of common argument names before falling
// back to empty (no detail, bullet reads as "● toolname").
//
// Rules, in order:
//  1. Empty input → empty string.
//  2. Recognised tool name → the field we've picked as its headline
//     (Read/Write/Edit → file_path; Bash → the first line of command;
//     Grep → pattern; WebFetch → url; TodoWrite → an item count; etc.).
//  3. Fallback: first non-empty short string value among a small pool
//     of common field names.
//  4. Anything longer than ~48 runes is truncated with a single "…".
func summarizeToolInput(name string, raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil || len(m) == 0 {
		return ""
	}
	switch strings.ToLower(name) {
	case "read", "write", "edit", "notebookedit", "notebook_edit":
		return trimSummary(stringField(m, "file_path", "notebook_path", "path"))
	case "bash":
		return trimSummary(firstLine(stringField(m, "command")))
	case "grep":
		if pat := stringField(m, "pattern"); pat != "" {
			return trimSummary(pat)
		}
		return trimSummary(stringField(m, "path"))
	case "glob":
		return trimSummary(stringField(m, "pattern", "path"))
	case "webfetch", "web_fetch":
		return trimSummary(stringField(m, "url"))
	case "websearch", "web_search":
		return trimSummary(stringField(m, "query"))
	case "task", "agent":
		if d := stringField(m, "description"); d != "" {
			return trimSummary(d)
		}
		return trimSummary(stringField(m, "subagent_type", "prompt"))
	case "todowrite", "todo_write":
		// A concrete count is more useful than a first-item snippet
		// when the list has many entries.
		if todos, ok := m["todos"].([]any); ok && len(todos) > 0 {
			return itemCount(len(todos), "todo", "todos")
		}
		return ""
	}
	return trimSummary(firstStringValue(m, "url", "path", "file_path", "query", "pattern", "command", "description", "prompt"))
}

// stringField returns the first non-empty string value in m among keys.
func stringField(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k].(string); ok {
			if s := strings.TrimSpace(v); s != "" {
				return s
			}
		}
	}
	return ""
}

// firstStringValue is the fallback scan for tools not in the name
// switch — same signature as stringField but the keys are the small
// "if you don't know what to say, try these" pool.
func firstStringValue(m map[string]any, keys ...string) string {
	return stringField(m, keys...)
}

// firstLine returns s up to the first newline (unwrapping trailing
// whitespace). Prevents a multi-line Bash command from spilling the
// bullet across the message.
func firstLine(s string) string {
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// trimSummary caps s at maxSummaryRunes runes, tacking on a single "…"
// when truncation happened. Rune-safe.
func trimSummary(s string) string {
	const maxSummaryRunes = 48
	if s == "" {
		return ""
	}
	r := []rune(s)
	if len(r) <= maxSummaryRunes {
		return s
	}
	return string(r[:maxSummaryRunes-1]) + "…"
}

// itemCount renders "1 todo" / "N todos" with correct pluralisation.
func itemCount(n int, singular, plural string) string {
	if n == 1 {
		return "1 " + singular
	}
	return strconv.Itoa(n) + " " + plural
}
