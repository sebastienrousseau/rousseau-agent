package cli

import (
	"context"
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/sebastienrousseau/rousseau-agent/internal/transport/telegram"
)

func newTelegramCmd(opts *Options) *cobra.Command {
	var (
		token     string
		allowlist []string
	)
	cmd := &cobra.Command{
		Use:   "telegram",
		Short: "Run the Telegram bot bridge",
		Long: "Requires a bot token from @BotFather. The daemon long-polls\n" +
			"getUpdates and routes each incoming private message through\n" +
			"the agent, replying via sendMessage. Allowlist is enforced\n" +
			"against the chat id as a string; zero entries allows anyone.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg := opts.Config
			tok := firstNonEmpty(token, cfg.Telegram.Token)
			if tok == "" {
				return errors.New("--token or telegram.token is required")
			}
			setUnattendedPermissionDefault(opts, "telegram")

			allow := allowlist
			if len(allow) == 0 {
				allow = cfg.Telegram.Allowlist
			}

			ctx := cmd.Context()
			wiring, err := assembleDaemon(ctx, opts, allow)
			if err != nil {
				return err
			}
			defer func() { _ = wiring.Sessions.Close() }()

			client, err := telegram.New(telegram.Config{
				Token:       tok,
				BaseURL:     cfg.Telegram.BaseURL,
				ReplyHeader: cfg.Telegram.ReplyHeader,
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

			opts.Logger.Info("telegram.starting", "allowlist", len(allow))
			return client.Start(ctx, wiring.Router)
		},
	}
	cmd.Flags().StringVar(&token, "token", "", "bot token (falls back to telegram.token in config)")
	cmd.Flags().StringSliceVar(&allowlist, "allow", nil, "restrict inbound to these chat ids")
	return cmd
}
