package builtin

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/sebastienrousseau/rousseau-agent/internal/tools"
)

// TestA2ADispatch_IsOutbound pins the tool as outbound: it sends the
// model's prompt to a third-party agent, so the daemon's default
// approver must gate it like every other send-shaped tool.
func TestA2ADispatch_IsOutbound(t *testing.T) {
	assert.True(t, tools.IsOutbound(NewA2ADispatchTool(nil)),
		"a2a_dispatch sends data to a peer agent and must report Outbound() == true")
}
