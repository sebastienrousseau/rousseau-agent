// Package skills loads user-authored skills from a directory and
// selects the ones relevant to an incoming user message.
//
// A skill is a Markdown file with an optional YAML-in-front-matter
// header that declares triggers (keywords), a short description, and
// the body that gets spliced into the system prompt when the skill
// activates. The format is deliberately close to the agentskills.io
// convention so files can be shared with other tools.
//
// Example skill file — `~/.local/share/rousseau/skills/git-rebase.md`:
//
//	---
//	name: git-rebase
//	description: Guide the user through an interactive rebase safely.
//	triggers: [rebase, git rebase, squash, autosquash]
//	---
//	When helping with a git rebase, first verify the current HEAD is
//	pushed to a remote branch. Prefer `git rebase -i --autosquash` when
//	the user has fixup commits. Never force-push to `main`.
package skills

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Skill is a loaded skill file.
type Skill struct {
	// Name identifies the skill; must match `^[a-z][a-z0-9-]*$`.
	Name string
	// Description is a one-line summary shown in `rousseau skills list`.
	Description string
	// Triggers activate the skill when one appears in a user message,
	// case-insensitively. Empty means the skill never auto-activates
	// (callers can still include it by name).
	Triggers []string
	// Body is the Markdown that gets spliced into the system prompt.
	Body string
	// Path is the file the skill was loaded from.
	Path string
}

// frontMatter is the parsed shape of the optional YAML header.
type frontMatter struct {
	Name        string   `yaml:"name"`
	Description string   `yaml:"description"`
	Triggers    []string `yaml:"triggers"`
}

// Load reads every *.md file under dir (non-recursive) and returns the
// parsed skills. A missing dir is not an error — Load returns nil.
func Load(dir string) ([]Skill, error) {
	if dir == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("skills: read dir: %w", err)
	}
	var out []Skill
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		s, err := loadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("skills: load %s: %w", e.Name(), err)
		}
		out = append(out, s)
	}
	return out, nil
}

func loadFile(path string) (Skill, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Skill{}, err
	}
	fm, body := splitFrontMatter(raw)
	s := Skill{
		Name: strings.TrimSuffix(filepath.Base(path), ".md"),
		Body: strings.TrimSpace(body),
		Path: path,
	}
	if fm != "" {
		var meta frontMatter
		if err := yaml.Unmarshal([]byte(fm), &meta); err != nil {
			return Skill{}, fmt.Errorf("parse front matter: %w", err)
		}
		if meta.Name != "" {
			s.Name = meta.Name
		}
		s.Description = meta.Description
		s.Triggers = meta.Triggers
	}
	return s, nil
}

// splitFrontMatter separates a leading `---\n…---\n` block from the
// rest of the file. Files without front matter return ("", raw).
func splitFrontMatter(raw []byte) (front, body string) {
	s := string(raw)
	if !strings.HasPrefix(s, "---\n") && !strings.HasPrefix(s, "---\r\n") {
		return "", s
	}
	trim := strings.TrimPrefix(strings.TrimPrefix(s, "---\r\n"), "---\n")
	end := strings.Index(trim, "\n---")
	if end < 0 {
		return "", s
	}
	front = trim[:end]
	// Skip the closing --- and any trailing newline.
	rest := trim[end+len("\n---"):]
	rest = strings.TrimPrefix(rest, "\r")
	rest = strings.TrimPrefix(rest, "\n")
	return front, rest
}

// Select returns the skills whose triggers appear in the user message
// (case-insensitive substring match). Returns skills in the order they
// were loaded, deduplicated by Name.
func Select(all []Skill, message string) []Skill {
	lower := strings.ToLower(message)
	seen := make(map[string]struct{}, len(all))
	var out []Skill
	for _, s := range all {
		if _, dup := seen[s.Name]; dup {
			continue
		}
		for _, t := range s.Triggers {
			if t == "" {
				continue
			}
			if strings.Contains(lower, strings.ToLower(t)) {
				out = append(out, s)
				seen[s.Name] = struct{}{}
				break
			}
		}
	}
	return out
}

// Compose renders a set of Skills as a system-prompt appendix. It is
// safe to call with an empty slice — returns "".
func Compose(activated []Skill) string {
	if len(activated) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n# Active skills\n\n")
	for _, s := range activated {
		fmt.Fprintf(&b, "## %s\n\n", s.Name)
		if s.Description != "" {
			fmt.Fprintf(&b, "*%s*\n\n", s.Description)
		}
		b.WriteString(s.Body)
		b.WriteString("\n\n")
	}
	return strings.TrimRight(b.String(), "\n")
}
