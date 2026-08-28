package skills

import (
	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
)

// Provider satisfies agent.SkillsProvider. It loads skills once from
// disk and, on each Session inspection, selects the ones triggered by
// the latest user text and composes them as a system-prompt appendix.
type Provider struct {
	skills []Skill
}

// NewProvider constructs a Provider from an already-loaded skill list.
// Empty input is fine — the Provider then contributes nothing.
func NewProvider(loaded []Skill) *Provider { return &Provider{skills: loaded} }

// FromDir loads skills from dir and wraps them in a Provider. Missing
// dir is not an error — the returned Provider is a no-op.
func FromDir(dir string) (*Provider, error) {
	loaded, err := Load(dir)
	if err != nil {
		return nil, err
	}
	return NewProvider(loaded), nil
}

// SystemAppendix satisfies agent.SkillsProvider.
func (p *Provider) SystemAppendix(s *agent.Session) string {
	if p == nil || len(p.skills) == 0 || s == nil {
		return ""
	}
	last, ok := lastUserText(s)
	if !ok {
		return ""
	}
	activated := Select(p.skills, last)
	return Compose(activated)
}

// Skills returns the loaded skills so callers (e.g. `rousseau skills
// list`) can render them.
func (p *Provider) Skills() []Skill {
	if p == nil {
		return nil
	}
	return p.skills
}

// lastUserText returns the concatenated text of the most recent user
// message. Returns (_, false) when the session carries no user text.
func lastUserText(s *agent.Session) (string, bool) {
	for i := len(s.Messages) - 1; i >= 0; i-- {
		m := s.Messages[i]
		if m.Role != agent.RoleUser {
			continue
		}
		var out string
		for _, c := range m.Content {
			if c.Kind == agent.ContentText && c.Text != "" {
				if out != "" {
					out += "\n"
				}
				out += c.Text
			}
		}
		if out != "" {
			return out, true
		}
	}
	return "", false
}
