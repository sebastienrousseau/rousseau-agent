// Package builtin ships the reference tools bundled with rousseau-agent.
package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/sebastienrousseau/rousseau-agent/internal/tools"
	"github.com/sebastienrousseau/rousseau-agent/internal/tools/fsguard"
)

// defaultReadMaxBytes caps a single read so `/dev/zero`, a FIFO or
// a multi-gigabyte log cannot take the daemon down with one call.
const defaultReadMaxBytes int64 = 4 << 20

// ReadTool reads a UTF-8 text file from the local filesystem.
type ReadTool struct {
	// Guard decides which paths may be read. Nil uses
	// fsguard.Default (deny list, no workspace root).
	Guard *fsguard.Guard
	// MaxBytes caps the file size. Zero uses 4 MiB.
	MaxBytes int64
}

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
	path, err := resolvePath(t.Guard, "read", in.Path)
	if err != nil {
		return "", err
	}
	limit := t.MaxBytes
	if limit <= 0 {
		limit = defaultReadMaxBytes
	}
	b, err := readRegular(t.Guard, path, limit)
	if err != nil {
		return "", fmt.Errorf("read: %s: %w", in.Path, err)
	}
	if !isLikelyText(b) {
		return "", fmt.Errorf("read: %s does not look like UTF-8 text", in.Path)
	}
	return string(b), nil
}

// readRegular reads at most limit bytes from a regular file, refusing
// directories, devices and FIFOs up front and a file that grows past
// the limit mid-read.
func readRegular(g *fsguard.Guard, path string, limit int64) ([]byte, error) {
	f, info, err := openRegular(g, path)
	if err != nil {
		return nil, err
	}
	defer f.Close() //nolint:errcheck // read-only handle
	if info.Size() > limit {
		return nil, fmt.Errorf("%d bytes, over the %d byte limit", info.Size(), limit)
	}
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("grew past the %d byte limit while reading", limit)
	}
	return b, nil
}

// openRegular opens path (already vetted by the guard) for reading
// without blocking on a FIFO, beneath the workspace root when one is
// set (so a swapped symlink cannot escape it), and refuses anything
// that is not a regular file. The caller closes the file.
func openRegular(g *fsguard.Guard, path string) (*os.File, os.FileInfo, error) {
	f, err := guardOrDefault(g).OpenRead(path, openFlags)
	if err != nil {
		return nil, nil, err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close() //nolint:errcheck // already failing
		return nil, nil, err
	}
	if !info.Mode().IsRegular() {
		_ = f.Close() //nolint:errcheck // already failing
		return nil, nil, errors.New("not a regular file")
	}
	return f, info, nil
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
