package skills

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Errors returned by ResolveResource so callers can distinguish
// path-safety violations from other filesystem errors.
var (
	// ErrResourceEscapesBase means the requested relative path
	// resolves outside the skill's base directory — a directory
	// traversal attempt or a mis-authored skill referencing an
	// external file. Never load such paths: the tier-3 contract
	// is "files in the skill dir, and nowhere else."
	ErrResourceEscapesBase = errors.New("skills: resource path escapes the skill base directory")
	// ErrResourceAbsolutePath means the caller passed an absolute
	// path rather than a relative one. Spec requires relative
	// paths from the skill root.
	ErrResourceAbsolutePath = errors.New("skills: resource path must be relative to the skill directory")
	// ErrResourceEmpty means the caller passed "" as the relative
	// path.
	ErrResourceEmpty = errors.New("skills: resource path is empty")
	// ErrResourceIsDir means the resolved path is a directory,
	// not a file — the tier-3 loader reads files individually,
	// not directory listings.
	ErrResourceIsDir = errors.New("skills: resource path resolves to a directory")
)

// ResolveResource canonicalises a relative path referenced from a
// SKILL.md body (tier 3) and returns the absolute path — but only
// when the result is genuinely inside baseDir. All directory-
// traversal, absolute-path, and symlink-escape attempts return
// ErrResourceEscapesBase and no filesystem access is performed
// beyond the safety check.
//
// The security model per spec: skills declare relative paths in
// their instructions ("run scripts/extract.py"); the harness turns
// those into filesystem reads on the model's behalf, and MUST
// refuse to reach outside the skill's directory. Without this
// check, a malicious skill body could exfiltrate /etc/passwd via
// a Read tool the model was told was scoped to the skill.
//
// Symlinks are resolved via filepath.EvalSymlinks before the final
// check, so a symlink inside the skill dir pointing to an external
// path is caught. When the file does not exist, the safety check
// still runs on the requested path so callers get
// ErrResourceEscapesBase (safety-first) rather than os.ErrNotExist
// (leaks nothing about paths outside the base).
func ResolveResource(baseDir, rel string) (string, error) {
	if rel == "" {
		return "", ErrResourceEmpty
	}
	if filepath.IsAbs(rel) {
		return "", ErrResourceAbsolutePath
	}
	if baseDir == "" {
		return "", errors.New("skills: base directory is empty")
	}

	// Canonicalise the base first — a caller may have passed a
	// relative or a symlink'd path. If EvalSymlinks fails
	// (base doesn't exist), fall back to Abs so the safety check
	// still runs on a well-formed path.
	absBase, err := filepath.Abs(baseDir)
	if err != nil {
		return "", fmt.Errorf("skills: canonicalise base: %w", err)
	}
	if evalBase, err := filepath.EvalSymlinks(absBase); err == nil {
		absBase = evalBase
	}

	// Join + clean to collapse any `.` / `..` in the relative
	// component. This is the pre-symlink-resolution safety check
	// — it catches trivial `../etc/passwd` before we ever touch
	// the filesystem.
	joined := filepath.Clean(filepath.Join(absBase, rel))
	if !insideDir(absBase, joined) {
		return "", ErrResourceEscapesBase
	}

	// Post-symlink-resolution safety check. When the file exists,
	// resolve any symlinks and verify the resolved path is still
	// inside the base. When it does not exist, we've already
	// checked the requested path above; the caller will get
	// os.ErrNotExist from their own Read.
	if resolved, err := filepath.EvalSymlinks(joined); err == nil {
		if !insideDir(absBase, resolved) {
			return "", ErrResourceEscapesBase
		}
		joined = resolved
	}

	// Reject directories: tier-3 loads files, one at a time.
	if fi, err := os.Stat(joined); err == nil && fi.IsDir() {
		return "", ErrResourceIsDir
	}

	return joined, nil
}

// insideDir reports whether child is genuinely inside parent (or
// equal to it). Both arguments MUST be absolute and cleaned;
// callers should have already run filepath.Abs + filepath.Clean.
//
// Uses filepath.Rel + a `..` prefix check because that's the only
// portable Go-stdlib way to detect a directory-escape after
// Clean has collapsed traversal — string-prefix comparison alone
// is unsafe (`/foo` vs `/foobar`).
func insideDir(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	// Rel returns `.` for the parent itself, `sub/foo` for a
	// child, and `../<x>` for anything above the parent.
	if rel == "." {
		return true
	}
	return !strings.HasPrefix(rel, "..")
}
