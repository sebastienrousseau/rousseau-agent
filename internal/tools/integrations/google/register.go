package google

import (
	"fmt"

	"github.com/sebastienrousseau/rousseau-agent/internal/tools"
)

// Register wires every Google Workspace tool into reg.
func Register(reg *tools.Registry, c *Client) error {
	for _, t := range []tools.Tool{
		NewGmailListTool(c),
		NewGmailGetTool(c),
		NewGmailSendTool(c),
		NewCalendarListEventsTool(c),
		NewCalendarCreateEventTool(c),
		NewDriveSearchTool(c),
		NewDriveGetTool(c),
	} {
		if err := reg.Register(t); err != nil {
			return fmt.Errorf("google: register %s: %w", t.Name(), err)
		}
	}
	return nil
}
