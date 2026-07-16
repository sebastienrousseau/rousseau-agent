package cli

import (
	"context"
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/sebastienrousseau/rousseau-agent/internal/transport/discord"
)

func newDiscordCmd(opts *Options) *cobra.Command {
	var (
		token     string
		allowlist []string
	)
	cmd := &cobra.Command{
		Use:   "discord",
		Short: "Run the Discord Gateway bridge",
		Long: "Requires a Discord bot token (developer portal) with the\n" +
			"Message Content intent enabled. The daemon connects to Discord's\n" +
			"Gateway WebSocket — no public HTTP surface exposed.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg := opts.Config
			tok := firstNonEmpty(token, cfg.Discord.Token)
			if tok == "" {
				return errors.New("discord.token is required")
			}
			setUnattendedPermissionDefault(opts, "discord")

			allow := allowlist
			if len(allow) == 0 {
				allow = cfg.Discord.Allowlist
			}

			ctx := cmd.Context()
			wiring, err := assembleDaemon(ctx, opts, allow)
			if err != nil {
				return err
			}
			defer func() { _ = wiring.Sessions.Close() }() //nolint:errcheck // best-effort cleanup

			client, err := discord.New(discord.Config{
				Token:       tok,
				ReplyHeader: cfg.Discord.ReplyHeader,
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

			opts.Logger.Info("discord.starting", "allowlist", len(allow))
			return client.Start(ctx, wiring.TransportHandler("discord", opts.Logger))
		},
	}
	cmd.Flags().StringVar(&token, "token", "", "bot token")
	cmd.Flags().StringSliceVar(&allowlist, "allow", nil, "restrict inbound to these Discord user IDs")
	return cmd
}
