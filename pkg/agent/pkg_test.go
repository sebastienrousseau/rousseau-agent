package agent_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/sebastienrousseau/rousseau-agent/pkg/agent"
	"github.com/sebastienrousseau/rousseau-agent/pkg/tools"
	"github.com/sebastienrousseau/rousseau-agent/pkg/tools/builtin"
)

// TestFacadeShape confirms every public alias resolves to a usable
// type so a Go-vet + typechecking upgrade in the underlying internal
// package can't silently break external consumers.
func TestFacadeShape(t *testing.T) {
	sess := agent.NewSession("test")
	assert.NotEmpty(t, sess.ID)

	msg := agent.NewUserText("hello")
	assert.Equal(t, agent.RoleUser, msg.Role)

	assistant := agent.NewAssistantText("hi back")
	assert.Equal(t, agent.RoleAssistant, assistant.Role)

	img := agent.NewUserImage("image/png", []byte{0x89, 0x50, 0x4E, 0x47}, "test")
	assert.Equal(t, agent.RoleUser, img.Role)
	assert.Equal(t, agent.ContentImage, img.Content[0].Kind)

	reg := tools.NewRegistry()
	assert.NoError(t, reg.Register(builtin.NewReadTool()))
	assert.NoError(t, reg.Register(builtin.NewWriteTool()))
	assert.NoError(t, reg.Register(builtin.NewEditTool()))
	assert.NoError(t, reg.Register(builtin.NewGrepTool(0, 0)))
	assert.Len(t, reg.Names(), 4)
}
