package composio

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/sebastienrousseau/rousseau-agent/internal/tools"
)

// TestAction_IsOutbound: a Composio action proxies an arbitrary
// third-party operation (send mail, post, create) and its name is not
// a trustworthy read/write signal, so every action is outbound (L-32).
func TestAction_IsOutbound(t *testing.T) {
	a := &action{spec: Action{Name: "GITHUB_LIST_REPOS", AppKey: "github"}, toolID: "cx_github_github_list_repos"}
	assert.True(t, tools.IsOutbound(a))
}
