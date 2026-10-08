package integrations_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/sebastienrousseau/rousseau-agent/internal/tools"
	"github.com/sebastienrousseau/rousseau-agent/internal/tools/integrations/github"
	"github.com/sebastienrousseau/rousseau-agent/internal/tools/integrations/google"
	"github.com/sebastienrousseau/rousseau-agent/internal/tools/integrations/linear"
	"github.com/sebastienrousseau/rousseau-agent/internal/tools/integrations/slack"
	"github.com/sebastienrousseau/rousseau-agent/internal/tools/integrations/stripe"
)

// TestOutboundClassification pins which native integration tools send
// data to a third party or change third-party state (L-32). A new
// tool must be added here with a deliberate classification.
func TestOutboundClassification(t *testing.T) {
	cases := []struct {
		tool     tools.Tool
		outbound bool
	}{
		{github.NewListReposTool(nil), false},
		{github.NewGetRepoTool(nil), false},
		{github.NewSearchCodeTool(nil), false},
		{github.NewListIssuesTool(nil), false},
		{github.NewGetIssueTool(nil), false},
		{github.NewCreateIssueTool(nil), true},
		{github.NewCommentIssueTool(nil), true},
		{github.NewListPRsTool(nil), false},
		{github.NewGetPRTool(nil), false},
		{google.NewGmailListTool(nil), false},
		{google.NewGmailGetTool(nil), false},
		{google.NewGmailSendTool(nil), true},
		{google.NewCalendarListEventsTool(nil), false},
		{google.NewCalendarCreateEventTool(nil), true},
		{google.NewDriveSearchTool(nil), false},
		{google.NewDriveGetTool(nil), false},
		{slack.NewPostMessageTool(nil), true},
		{slack.NewAddReactionTool(nil), true},
		{slack.NewGetThreadTool(nil), false},
		{slack.NewListChannelsTool(nil), false},
		{linear.NewListIssuesTool(nil), false},
		{linear.NewGetIssueTool(nil), false},
		{linear.NewCreateIssueTool(nil), true},
		{linear.NewUpdateIssueTool(nil), true},
		{stripe.NewListChargesTool(nil), false},
		{stripe.NewGetCustomerTool(nil), false},
	}
	for _, c := range cases {
		t.Run(c.tool.Name(), func(t *testing.T) {
			assert.Equal(t, c.outbound, tools.IsOutbound(c.tool))
		})
	}
}
