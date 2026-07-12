package cli

import (
	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
	"github.com/sebastienrousseau/rousseau-agent/internal/skills"
	sqlitestore "github.com/sebastienrousseau/rousseau-agent/internal/state/sqlite"
)

// buildSkillsProvider loads skills from disk (empty dir is fine — the
// returned provider becomes a no-op) and adapts them to the agent's
// SkillsProvider seam.
func buildSkillsProvider(opts *Options) (agent.SkillsProvider, error) {
	dir := resolveSkillsDir(opts)
	if dir == "" {
		return nil, nil
	}
	p, err := skills.FromDir(dir)
	if err != nil {
		return nil, err
	}
	return p, nil
}

// buildRecallProvider constructs an FTS-backed recall provider from
// the sqlite store, skipping the current session's own snippets.
func buildRecallProvider(store *sqlitestore.Store) agent.RecallProvider {
	if store == nil {
		return nil
	}
	return &agent.FTSRecall{
		Searcher:      sqlitestore.NewRecallSearcher(store),
		SkipSessionID: func(s *agent.Session) string { return s.ID },
	}
}
