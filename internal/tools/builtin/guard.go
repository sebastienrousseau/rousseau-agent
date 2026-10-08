package builtin

import (
	"fmt"

	"github.com/sebastienrousseau/rousseau-agent/internal/tools/fsguard"
)

// resolvePath runs a model-supplied path through the tool's guard
// (or the process default when none was injected) and returns the
// real path the tool must operate on. The tool name prefixes the
// error so the model sees which call was refused and why.
func resolvePath(g *fsguard.Guard, tool, path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("%s: path is required", tool)
	}
	if g == nil {
		g = fsguard.Default()
	}
	real, err := g.Resolve(path)
	if err != nil {
		return "", fmt.Errorf("%s: %w", tool, err)
	}
	return real, nil
}

// resolveWritePath is resolvePath for tools that modify files: it also
// refuses the write-only deny list (shell start-up files, autostart
// entries, git hooks).
func resolveWritePath(g *fsguard.Guard, tool, path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("%s: path is required", tool)
	}
	if g == nil {
		g = fsguard.Default()
	}
	real, err := g.ResolveForWrite(path)
	if err != nil {
		return "", fmt.Errorf("%s: %w", tool, err)
	}
	return real, nil
}

// guardOrDefault returns g, or the process-wide default guard.
func guardOrDefault(g *fsguard.Guard) *fsguard.Guard {
	if g == nil {
		return fsguard.Default()
	}
	return g
}
