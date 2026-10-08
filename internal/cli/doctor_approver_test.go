package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/config"
)

func TestCheckApproverPatterns_WarnsOncePerUnanchoredAllow(t *testing.T) {
	cfg := &config.Config{}
	cfg.Agent.Approver.Mode = "pattern"
	cfg.Agent.Approver.Allow = []config.PatternEntry{
		{Tool: "bash", Match: "git status"},
		{Tool: "bash", Field: "command", Match: "ls"},
		{Tool: "read"},
	}
	cfg.Agent.Approver.Deny = []config.PatternEntry{{Tool: "bash", Match: "rm -rf"}}

	rows := checkApproverPatterns(cfg)
	require.Len(t, rows, 2, "one warning per allow rule without field; deny and field rules are not warned")
	for _, r := range rows {
		assert.Equal(t, "warn", r.Status)
		assert.Contains(t, r.Name, "approver.pattern.allow")
	}
	assert.Contains(t, rows[0].Detail, "bash")
	assert.Contains(t, rows[1].Detail, "read")
}

func TestCheckApproverPatterns_SilentOutsidePatternMode(t *testing.T) {
	cfg := &config.Config{}
	cfg.Agent.Approver.Allow = []config.PatternEntry{{Tool: "bash", Match: "x"}}
	assert.Empty(t, checkApproverPatterns(cfg))
}

func TestToRules_MapsField(t *testing.T) {
	got := toRules([]config.PatternEntry{{Tool: "bash", Match: "ls", Field: "command"}})
	require.Len(t, got, 1)
	assert.Equal(t, "command", got[0].Field)
	assert.Equal(t, "bash", got[0].ToolName)
}
