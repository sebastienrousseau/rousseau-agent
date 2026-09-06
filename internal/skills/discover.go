package skills

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// DiscoverOptions tunes how DiscoverSpec walks the filesystem.
// Zero-value picks the spec's recommended defaults.
type DiscoverOptions struct {
	// MaxDepth caps how deeply the walker descends from Root.
	// Zero uses the spec-recommended default of 6. Skill dirs
	// deeper than this are silently skipped.
	MaxDepth int
	// MaxDirs caps the total number of directories inspected in
	// one call, guarding against runaway walks (mounted /proc,
	// pathological trees). Zero uses the default of 2000.
	MaxDirs int
	// Skip lists directory names never descended into (e.g.
	// ".git", "node_modules"). Zero uses a spec-recommended
	// default set.
	Skip map[string]struct{}
	// OnInvalid, when non-nil, is called for each discovered
	// SKILL.md that fails to parse or validate. The default
	// (nil) silently skips invalid skills — legitimate spec
	// behaviour, since a bad third-party skill shouldn't wedge
	// the daemon. Set this in tests or `rousseau skills
	// validate` to surface the errors.
	OnInvalid func(path string, err error)
}

// defaultSkipDirs mirrors the spec's recommended skip list.
// Kept as a var so tests can extend it without racing.
var defaultSkipDirs = map[string]struct{}{
	".git":         {},
	"node_modules": {},
	".venv":        {},
	"vendor":       {},
	"__pycache__":  {},
}

// DiscoverSpec walks root looking for `<any-subdir>/SKILL.md` files
// and returns every one that parses + validates successfully. A
// missing root is not an error — the function returns nil.
//
// Ordering: skills are returned in the deterministic order
// filepath.WalkDir emits (lexicographic per directory), so callers
// that render a catalog see stable output across runs.
//
// The walker never follows symlinks (silent skip). Symlink loops
// therefore cannot wedge discovery.
func DiscoverSpec(root string, opts DiscoverOptions) ([]SkillSpec, error) {
	if root == "" {
		return nil, nil
	}
	if _, err := os.Stat(root); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("skills: stat discovery root %q: %w", root, err)
	}

	maxDepth := opts.MaxDepth
	if maxDepth <= 0 {
		maxDepth = 6
	}
	maxDirs := opts.MaxDirs
	if maxDirs <= 0 {
		maxDirs = 2000
	}
	skip := opts.Skip
	if skip == nil {
		skip = defaultSkipDirs
	}

	var found []SkillSpec
	dirCount := 0

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			// Unreadable subtree — skip rather than abort the whole scan.
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		// Symlink-to-directory: don't descend (WalkDir already
		// resolves symlinks on the initial stat but not on
		// recursion; being explicit here keeps behaviour stable
		// across Go versions).
		if info, ierr := d.Info(); ierr == nil && info.Mode()&os.ModeSymlink != 0 {
			return fs.SkipDir
		}
		// Depth check: relative to root, count separators.
		rel, rerr := filepath.Rel(root, path)
		if rerr == nil && rel != "." {
			depth := strings.Count(rel, string(filepath.Separator)) + 1
			if depth > maxDepth {
				return fs.SkipDir
			}
		}
		// Skip-list check.
		if _, banned := skip[d.Name()]; banned && path != root {
			return fs.SkipDir
		}
		dirCount++
		if dirCount > maxDirs {
			return fmt.Errorf("skills: discovery scanned more than %d directories under %q (raise MaxDirs, or narrow the root)", maxDirs, root)
		}

		// Look for SKILL.md (spec-canonical) or skill.md (leniency
		// fallback per the reference validator).
		skillPath := findSkillFile(path)
		if skillPath == "" {
			return nil
		}
		raw, rerr := os.ReadFile(skillPath)
		if rerr != nil {
			if opts.OnInvalid != nil {
				opts.OnInvalid(skillPath, fmt.Errorf("read: %w", rerr))
			}
			return nil
		}
		spec, perr := ParseSkillMD(raw)
		if perr != nil {
			if opts.OnInvalid != nil {
				opts.OnInvalid(skillPath, perr)
			}
			return nil
		}
		if verr := ValidateSpec(spec, filepath.Base(path)); verr != nil {
			if opts.OnInvalid != nil {
				opts.OnInvalid(skillPath, verr)
			}
			return nil
		}
		spec.BaseDir = path
		found = append(found, spec)
		return nil
	})
	if err != nil {
		return found, err
	}
	return found, nil
}

// findSkillFile looks in dir for SKILL.md (spec-canonical) and
// falls back to skill.md (reference validator leniency). Returns
// the absolute path of the file, or "" when neither is present.
//
// The fallback is important on case-insensitive filesystems
// (macOS default, Windows) where both names resolve to the same
// file; the fallback also catches skills authored on tooling that
// lower-cases file names.
func findSkillFile(dir string) string {
	for _, name := range []string{"SKILL.md", "skill.md"} {
		p := filepath.Join(dir, name)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p
		}
	}
	return ""
}
