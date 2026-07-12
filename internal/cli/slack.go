package cli

import (
	"context"
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/sebastienrousseau/rousseau-agent/internal/transport/slack"
)

func newSlackCmd(opts *Options) *cobra.Command {
	var (
		appToken  string
		botToken  string
		botUserID string
		allowlist []string
	)
	cmd := &cobra.Command{
		Use:   "slack",
		Short: "Run the Slack bridge via Socket Mode",
		Long: "Requires a Slack app with Socket Mode enabled and two tokens:\n" +
			"  - app_token (xapp-*) with connections:write\n" +
			"  - bot_token (xoxb-*) with chat:write and message event subscriptions\n\n" +
			"No public HTTP surface is exposed — the Socket Mode WebSocket is\n" +
			"outbound from the daemon.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg := opts.Config
			app := firstNonEmpty(appToken, cfg.Slack.AppToken)
			bot := firstNonEmpty(botToken, cfg.Slack.BotToken)
			if app == "" || bot == "" {
				return errors.New("slack.app_token and slack.bot_token are required")
			}
			setUnattendedPermissionDefault(opts, "slack")

			allow := allowlist
			if len(allow) == 0 {
				allow = cfg.Slack.Allowlist
			}

			ctx := cmd.Context()
			wiring, err := assembleDaemon(ctx, opts, allow)
			if err != nil {
				return err
			}
			defer func() { _ = wiring.Sessions.Close() }() //nolint:errcheck // best-effort cleanup

			client, err := slack.New(slack.Config{
				AppToken:    app,
				BotToken:    bot,
				BotUserID:   firstNonEmpty(botUserID, cfg.Slack.BotUserID),
				ReplyHeader: cfg.Slack.ReplyHeader,
			}, opts.Logger)
			if err != nil {
				return err
			}

			shutdown, err := wiring.startCron(ctx, func(dctx context.Context, target, body string) error {
				return client.Deliver(dctx, target, body)
			}, opts.Logger)
			if err != nil {
				return fmt.Errorf("cron: %w", err)
			}
			defer shutdown()

			opts.Logger.Info("slack.starting", "allowlist", len(allow))
			return client.Start(ctx, wiring.Router)
		},
	}
	cmd.Flags().StringVar(&appToken, "app-token", "", "xapp-* app-level token")
	cmd.Flags().StringVar(&botToken, "bot-token", "", "xoxb-* bot token")
	cmd.Flags().StringVar(&botUserID, "bot-user-id", "", "bot user ID for own-message loop prevention")
	cmd.Flags().StringSliceVar(&allowlist, "allow", nil, "restrict inbound to these Slack user IDs")
	return cmd
}
