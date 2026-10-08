package builtin

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/sebastienrousseau/rousseau-agent/internal/tools"
	"github.com/sebastienrousseau/rousseau-agent/internal/tools/fsguard"
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
	// Guard decides which directories may be searched. Nil uses
	// fsguard.Default (deny list, no workspace root).
	Guard *fsguard.Guard
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
	root, err := resolvePath(t.Guard, "grep", in.Path)
	if err != nil {
		return "", err
	}

	pat := in.Pattern
	if in.IgnoreCase {
		pat = "(?i)" + pat
	}
	re, err := regexp.Compile(pat)
	if err != nil {
		return "", fmt.Errorf("grep: compile pattern: %w", err)
	}

	if in.Include != "" {
		if _, err := filepath.Match(in.Include, ""); err != nil {
			return "", fmt.Errorf("grep: bad include glob %q: %w", in.Include, err)
		}
	}
	run := &grepRun{t: t, guard: guardOrDefault(t.Guard), re: re, include: in.Include, root: root}
	walkErr := filepath.WalkDir(root, run.visit(ctx))
	if walkErr != nil && !errors.Is(walkErr, fs.SkipAll) {
		return run.out.String(), walkErr
	}
	return run.result(), nil
}

// grepMaxOutputBytes caps the total text grep returns; grepMaxLineBytes
// caps one reported line.
const (
	grepMaxOutputBytes = 64 << 10
	grepMaxLineBytes   = 512
)

// grepRun is one search. Every walked entry is checked against the
// guard, so a search rooted at an allowed directory never descends
// into a denied one (~/.ssh under $HOME) or out of the workspace.
type grepRun struct {
	t       *GrepTool
	guard   *fsguard.Guard
	re      *regexp.Regexp
	include string
	root    string
	out     strings.Builder
	matches int
}

func (r *grepRun) visit(ctx context.Context) fs.WalkDirFunc {
	return func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // skip inaccessible entries; grep should degrade gracefully
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.IsDir() {
			return r.enterDir(p, d)
		}
		if r.full() {
			return fs.SkipAll
		}
		if r.wanted(p, d) {
			r.searchFile(p)
		}
		return nil
	}
}

// enterDir skips vendored and VCS directories and anything the guard
// refuses.
func (r *grepRun) enterDir(p string, d fs.DirEntry) error {
	if p == r.root {
		return nil
	}
	if shouldSkipDir(d.Name()) {
		return filepath.SkipDir
	}
	if _, err := r.guard.Resolve(p); err != nil {
		return filepath.SkipDir
	}
	return nil
}

// wanted reports whether a non-directory entry should be searched:
// a regular file (never a symlink, FIFO, socket or device) that the
// guard allows, matches include, and is within the size cap.
func (r *grepRun) wanted(p string, d fs.DirEntry) bool {
	if !d.Type().IsRegular() {
		return false
	}
	if _, err := r.guard.Resolve(p); err != nil {
		return false
	}
	if r.include != "" {
		if ok, _ := filepath.Match(r.include, d.Name()); !ok { //nolint:errcheck // a bad glob matches nothing
			return false
		}
	}
	info, err := d.Info()
	return err == nil && info.Size() <= r.t.MaxFileBytes
}

func (r *grepRun) full() bool {
	return r.matches >= r.t.MaxMatches || r.out.Len() >= grepMaxOutputBytes
}

func (r *grepRun) result() string {
	switch {
	case r.matches == 0:
		return "no matches"
	case r.matches >= r.t.MaxMatches:
		fmt.Fprintf(&r.out, "(truncated at %d matches)\n", r.t.MaxMatches)
	case r.out.Len() >= grepMaxOutputBytes:
		fmt.Fprintf(&r.out, "(truncated at %d bytes)\n", grepMaxOutputBytes)
	}
	return r.out.String()
}

func (r *grepRun) searchFile(path string) {
	f, _, err := openRegular(r.guard, path)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }() //nolint:errcheck // best-effort cleanup

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for lineNo := 1; scanner.Scan() && !r.full(); lineNo++ {
		line := scanner.Text()
		if strings.ContainsRune(line, '\x00') {
			return
		}
		if r.re.MatchString(line) {
			if len(line) > grepMaxLineBytes {
				line = line[:grepMaxLineBytes] + "…"
			}
			fmt.Fprintf(&r.out, "%s:%d: %s\n", path, lineNo, line)
			r.matches++
		}
	}
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
