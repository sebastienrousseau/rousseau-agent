package builtin

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/sebastienrousseau/rousseau-agent/internal/tools"
)

// GrepTool scans files under a root path for a regular expression and
// returns matching lines. It is intentionally simpler than ripgrep — the
// goal is a dependency-free grep that runs from within the process.
type GrepTool struct {
	// MaxMatches caps how many matches to report. Zero uses 200.
	MaxMatches int
	// MaxFileBytes caps how large a single file may be to scan.
	// Zero uses 4 MiB.
	MaxFileBytes int64
}

// NewGrepTool constructs a GrepTool with the given limits. Zero uses the
// defaults.
func NewGrepTool(maxMatches int, maxFileBytes int64) *GrepTool {
	if maxMatches == 0 {
		maxMatches = 200
	}
	if maxFileBytes == 0 {
		maxFileBytes = 4 << 20
	}
	return &GrepTool{MaxMatches: maxMatches, MaxFileBytes: maxFileBytes}
}

// Name returns the tool identifier.
func (*GrepTool) Name() string { return "grep" }

// Description returns the model-facing description.
func (*GrepTool) Description() string {
	return "Search files under a directory for a Go regular expression. Skips binary files and files larger than the configured limit. Returns 'path:line: matched_line' rows."
}

// InputSchema returns the tool's input JSON Schema.
func (*GrepTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"pattern": map[string]any{
				"type":        "string",
				"description": "Go RE2 regular expression to match.",
			},
			"path": map[string]any{
				"type":        "string",
				"description": "Absolute directory to search under.",
			},
			"include": map[string]any{
				"type":        "string",
				"description": "Optional filename glob (e.g. '*.go'). Applied to the base name.",
			},
			"ignore_case": map[string]any{
				"type":        "boolean",
				"description": "Case-insensitive match. Defaults to false.",
			},
		},
		"required": []string{"pattern", "path"},
	}
}

type grepInput struct {
	Pattern    string `json:"pattern"`
	Path       string `json:"path"`
	Include    string `json:"include,omitempty"`
	IgnoreCase bool   `json:"ignore_case,omitempty"`
}

// Execute runs the tool.
func (t *GrepTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var in grepInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return "", fmt.Errorf("grep: parse input: %w", err)
	}
	if in.Pattern == "" {
		return "", fmt.Errorf("grep: pattern is required")
	}
	if in.Path == "" {
		return "", fmt.Errorf("grep: path is required")
	}
	if !filepath.IsAbs(in.Path) {
		return "", fmt.Errorf("grep: path must be absolute, got %q", in.Path)
	}

	pat := in.Pattern
	if in.IgnoreCase {
		pat = "(?i)" + pat
	}
	re, err := regexp.Compile(pat)
	if err != nil {
		return "", fmt.Errorf("grep: compile pattern: %w", err)
	}

	var out strings.Builder
	matches := 0

	walkErr := filepath.WalkDir(in.Path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.IsDir() {
			if shouldSkipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if in.Include != "" {
			ok, mErr := filepath.Match(in.Include, d.Name())
			if mErr != nil {
				return fmt.Errorf("grep: bad include glob %q: %w", in.Include, mErr)
			}
			if !ok {
				return nil
			}
		}
		info, statErr := d.Info()
		if statErr != nil {
			return nil
		}
		if info.Size() > t.MaxFileBytes {
			return nil
		}
		if matches >= t.MaxMatches {
			return fs.SkipAll
		}
		return searchFile(p, re, &out, &matches, t.MaxMatches)
	})
	if walkErr != nil && !errors.Is(walkErr, fs.SkipAll) {
		return out.String(), walkErr
	}
	if matches == 0 {
		return "no matches", nil
	}
	if matches >= t.MaxMatches {
		fmt.Fprintf(&out, "(truncated at %d matches)\n", t.MaxMatches)
	}
	return out.String(), nil
}

func searchFile(path string, re *regexp.Regexp, out *strings.Builder, matches *int, cap int) error {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := scanner.Text()
		if strings.ContainsRune(line, '\x00') {
			return nil
		}
		if re.MatchString(line) {
			fmt.Fprintf(out, "%s:%d: %s\n", path, lineNo, line)
			*matches++
			if *matches >= cap {
				return nil
			}
		}
	}
	return nil
}

func shouldSkipDir(name string) bool {
	switch name {
	case ".git", "node_modules", "vendor", ".venv", "__pycache__", "dist", "build":
		return true
	}
	return false
}

// Compile-time interface satisfaction check.
var _ tools.Tool = (*GrepTool)(nil)
