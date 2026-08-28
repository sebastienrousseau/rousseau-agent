package github

import (
	"fmt"

	"github.com/sebastienrousseau/rousseau-agent/internal/tools"
)

// Register wires every GitHub tool into reg. Call once at daemon
// startup after building a Client. Returns the first registration
// error so operators see it before serving traffic.
func Register(reg *tools.Registry, c *Client) error {
	all := []tools.Tool{
		NewListReposTool(c),
		NewGetRepoTool(c),
		NewSearchCodeTool(c),
		NewListPRsTool(c),
		NewGetPRTool(c),
		NewListIssuesTool(c),
		NewGetIssueTool(c),
		NewCreateIssueTool(c),
		NewCommentIssueTool(c),
	}
	for _, t := range all {
		if err := reg.Register(t); err != nil {
			return fmt.Errorf("github: register %s: %w", t.Name(), err)
		}
	}
	return nil
}
