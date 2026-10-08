// Package fsguard decides which filesystem paths the model-callable
// file tools (read, write, edit, grep) may touch.
//
// Two rules, both evaluated on the symlink-resolved path so a link
// planted inside the workspace cannot point out of it:
//
//  1. A deny list of locations that are never legitimate tool targets
//     regardless of workspace: the daemon's own config and state (API
//     keys, sender allow-lists, OPA policy, allowed-signers), the
//     operator's SSH, GPG, cloud and container credentials, and the
//     kernel pseudo-filesystems. [DefaultDeny] lists them; operators
//     extend the list via tools.fs.deny.
//  2. An optional workspace root. When set, every path must resolve
//     to the root or below it.
//
// The threat model is prompt injection: a message on any transport
// convinces the model to read config.yaml or append to
// authorized_keys. The guard is not a defence against a local attacker
// who already has filesystem access and can race a symlink swap
// between Resolve and the file operation.
package fsguard

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

// ErrRelative is returned for a path that is not absolute.
var ErrRelative = errors.New("fsguard: path must be absolute")

// ErrDenied is returned for a path under a denied location.
var ErrDenied = errors.New("fsguard: path is on the deny list")

// ErrWriteDenied is returned for a write to a path that is readable
// but would let a write run code outside any sandbox (shell start-up
// files, autostart entries, git hooks).
var ErrWriteDenied = errors.New("fsguard: path may not be written")

// ErrOutsideRoot is returned for a path outside the workspace root.
var ErrOutsideRoot = errors.New("fsguard: path is outside the workspace root")

// Guard is an immutable path policy. The zero value denies nothing
// and has no root; use [Default] or [New].
type Guard struct {
	root      string
	deny      []string
	writeDeny []string
}

// New builds a guard with an optional workspace root and the default
// deny list plus extraDeny. Root and deny entries are symlink-resolved
// where they exist so comparisons happen on real paths. An empty root
// means "any path not on the deny list". The only error is a relative
// root or deny entry.
func New(root string, extraDeny []string) (*Guard, error) {
	if root != "" && !filepath.IsAbs(root) {
		return nil, fmt.Errorf("fsguard: root %q: %w", root, ErrRelative)
	}
	for _, d := range extraDeny {
		if d != "" && !filepath.IsAbs(d) {
			return nil, fmt.Errorf("fsguard: deny entry %q: %w", d, ErrRelative)
		}
	}
	return newGuard(root, append(DefaultDeny(), extraDeny...)), nil
}

// caseInsensitiveFS is true where the default file systems compare
// names case-insensitively (APFS, NTFS), so "~/.SSH" must match the
// "~/.ssh" deny entry.
var caseInsensitiveFS = runtime.GOOS == "darwin" || runtime.GOOS == "windows"

// newGuard resolves already-validated absolute entries.
func newGuard(root string, deny []string) *Guard {
	g := &Guard{}
	if root != "" {
		g.root = resolveExisting(filepath.Clean(root))
	}
	for _, d := range deny {
		if d != "" {
			g.deny = append(g.deny, resolveExisting(filepath.Clean(d)))
		}
	}
	for _, d := range DefaultWriteDeny() {
		g.writeDeny = append(g.writeDeny, resolveExisting(filepath.Clean(d)))
	}
	return g
}

var (
	defaultOnce  sync.Once
	defaultGuard *Guard
)

// Default returns the process-wide guard with no root and the default
// deny list. Tools fall back to it when none was injected, so the
// deny list applies even to library consumers that never configure
// one.
func Default() *Guard {
	defaultOnce.Do(func() { defaultGuard = newGuard("", DefaultDeny()) })
	return defaultGuard
}

// DefaultDeny returns the built-in deny list for the current user.
// Entries that do not exist on this host are still listed so a later
// creation is covered.
func DefaultDeny() []string {
	out := []string{"/proc", "/sys", "/dev", "/etc/shadow", "/etc/sudoers", "/etc/sudoers.d"}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return out
	}
	cfgHome := os.Getenv("XDG_CONFIG_HOME")
	if cfgHome == "" {
		cfgHome = filepath.Join(home, ".config")
	}
	dataHome := os.Getenv("XDG_DATA_HOME")
	if dataHome == "" {
		dataHome = filepath.Join(home, ".local", "share")
	}
	out = append(out,
		filepath.Join(cfgHome, "rousseau"),
		filepath.Join(dataHome, "rousseau"),
		filepath.Join(home, ".config", "rousseau"),
		filepath.Join(home, ".local", "share", "rousseau"),
		filepath.Join(home, ".local", "state", "rousseau"), // audit chain key
		filepath.Join(stateHome(home), "rousseau"),         // audit chain key under $XDG_STATE_HOME
		filepath.Join(home, ".ssh"),
		filepath.Join(home, ".gnupg"),
		filepath.Join(home, ".aws"),
		filepath.Join(home, ".azure"),
		filepath.Join(home, ".kube"),
		filepath.Join(home, ".docker"),
		filepath.Join(home, ".claude"),
		filepath.Join(home, ".netrc"),
		filepath.Join(home, ".git-credentials"),
		filepath.Join(cfgHome, "gh"),
		filepath.Join(cfgHome, "gcloud"),
		filepath.Join(home, ".npmrc"),
		filepath.Join(home, ".pypirc"),
		filepath.Join(home, ".cargo", "credentials"),
		filepath.Join(home, ".cargo", "credentials.toml"),
		filepath.Join(home, ".password-store"),
		filepath.Join(home, ".bash_history"),
		filepath.Join(home, ".zsh_history"),
		filepath.Join(dataHome, "fish", "fish_history"),
	)
	return out
}

// stateHome is $XDG_STATE_HOME, or ~/.local/state when unset.
func stateHome(home string) string {
	if d := os.Getenv("XDG_STATE_HOME"); d != "" {
		return d
	}
	return filepath.Join(home, ".local", "state")
}

// DefaultWriteDeny lists paths the file tools may read but never
// write: a write there runs code outside any sandbox at the next
// login, shell start or git operation. Any path with a ".git/hooks"
// segment is refused as well (see [Guard.ResolveForWrite]).
func DefaultWriteDeny() []string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return nil
	}
	cfgHome := os.Getenv("XDG_CONFIG_HOME")
	if cfgHome == "" {
		cfgHome = filepath.Join(home, ".config")
	}
	return []string{
		filepath.Join(home, ".bashrc"),
		filepath.Join(home, ".bash_profile"),
		filepath.Join(home, ".bash_login"),
		filepath.Join(home, ".profile"),
		filepath.Join(home, ".zshrc"),
		filepath.Join(home, ".zprofile"),
		filepath.Join(home, ".zshenv"),
		filepath.Join(home, ".zlogin"),
		filepath.Join(cfgHome, "fish"),
		filepath.Join(cfgHome, "systemd", "user"),
		filepath.Join(cfgHome, "autostart"),
		filepath.Join(cfgHome, "environment.d"),
		filepath.Join(home, ".local", "bin"),
		filepath.Join(home, "Library", "LaunchAgents"),
	}
}

// Root returns the workspace root, or "" when unrestricted.
func (g *Guard) Root() string { return g.root }

// Resolve validates path and returns its symlink-resolved absolute
// form, which callers should use for the actual file operation.
func (g *Guard) Resolve(path string) (string, error) {
	if path == "" || !filepath.IsAbs(path) {
		return "", fmt.Errorf("%w: %q", ErrRelative, path)
	}
	real := resolveExisting(filepath.Clean(path))
	for _, d := range g.deny {
		if within(real, d) {
			return "", fmt.Errorf("%w: %s", ErrDenied, path)
		}
	}
	if g.root != "" && !within(real, g.root) {
		return "", fmt.Errorf("%w: %s (root %s)", ErrOutsideRoot, path, g.root)
	}
	return real, nil
}

// ResolveForWrite is Resolve plus the write-only deny list and the
// ".git/hooks" rule. write and edit use it; read and grep do not.
func (g *Guard) ResolveForWrite(path string) (string, error) {
	real, err := g.Resolve(path)
	if err != nil {
		return "", err
	}
	for _, d := range g.writeDeny {
		if within(real, d) {
			return "", fmt.Errorf("%w: %s", ErrWriteDenied, path)
		}
	}
	if hasGitHooksSegment(real) {
		return "", fmt.Errorf("%w: %s (git hooks run on the next git operation)", ErrWriteDenied, path)
	}
	return real, nil
}

// hasGitHooksSegment reports whether p contains ".git/hooks".
func hasGitHooksSegment(p string) bool {
	sep := string(filepath.Separator)
	needle := sep + ".git" + sep + "hooks"
	if caseInsensitiveFS {
		p, needle = strings.ToLower(p), strings.ToLower(needle)
	}
	return strings.Contains(p+sep, needle+sep)
}

// within reports whether p equals base or sits below it.
func within(p, base string) bool {
	if caseInsensitiveFS {
		p, base = strings.ToLower(p), strings.ToLower(base)
	}
	if p == base {
		return true
	}
	sep := string(filepath.Separator)
	if base == sep {
		return strings.HasPrefix(p, sep)
	}
	return strings.HasPrefix(p, base+sep)
}

// resolveExisting symlink-resolves the longest existing ancestor of
// the absolute, cleaned path p and re-attaches the non-existent tail,
// so a path that is about to be created still compares on its real
// parent directory. Walking up always terminates at the filesystem
// root; should even that fail to resolve, the cleaned path is
// returned unchanged.
func resolveExisting(p string) string {
	var tail []string
	cur := p
	for {
		if r, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.Join(append([]string{r}, tail...)...)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return p
		}
		tail = append([]string{filepath.Base(cur)}, tail...)
		cur = parent
	}
}
