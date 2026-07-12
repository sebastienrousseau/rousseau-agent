package cli

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/sebastienrousseau/rousseau-agent/internal/transport/imessage"
)

func newIMessageCmd(opts *Options) *cobra.Command {
	var (
		baseURL      string
		password     string
		pollInterval string
	)
	cmd := &cobra.Command{
		Use:   "imessage",
		Short: "Run the iMessage bridge via a BlueBubbles server",
		Long: "Requires a BlueBubbles (https://bluebubbles.app) server reachable\n" +
			"over HTTP with a password. The daemon polls /api/v1/message for\n" +
			"new messages and posts replies to /api/v1/message/text.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg := opts.Config
			base := firstNonEmpty(baseURL, cfg.IMessage.BaseURL)
			pass := firstNonEmpty(password, cfg.IMessage.Password)
			if base == "" || pass == "" {
				return errors.New("imessage.base_url and imessage.password are required")
			}
			setUnattendedPermissionDefault(opts, "imessage")

			ctx := cmd.Context()
			wiring, err := assembleDaemon(ctx, opts, nil)
			if err != nil {
				return err
			}
			defer func() { _ = wiring.Sessions.Close() }() //nolint:errcheck // best-effort cleanup

			poll := 0 * time.Second
			if s := firstNonEmpty(pollInterval, cfg.IMessage.PollInterval); s != "" {
				d, err := time.ParseDuration(s)
				if err != nil {
					return fmt.Errorf("imessage: poll_interval: %w", err)
				}
				poll = d
			}

			client, err := imessage.New(imessage.Config{
				BaseURL:      base,
				Password:     pass,
				ReplyHeader:  cfg.IMessage.ReplyHeader,
				PollInterval: poll,
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

			opts.Logger.Info("imessage.starting", "base", base)
			return client.Start(ctx, wiring.Router)
		},
	}
	cmd.Flags().StringVar(&baseURL, "base-url", "", "BlueBubbles server URL, e.g. http://localhost:1234")
	cmd.Flags().StringVar(&password, "password", "", "BlueBubbles server password")
	cmd.Flags().StringVar(&pollInterval, "poll-interval", "", "polling cadence, e.g. 5s")
	return cmd
}
