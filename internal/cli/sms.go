package cli

import (
	"context"
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/sebastienrousseau/rousseau-agent/internal/transport/sms"
)

func newSMSCmd(opts *Options) *cobra.Command {
	var (
		provider   string
		from       string
		accountSID string
		authToken  string
		apiKey     string
	)
	cmd := &cobra.Command{
		Use:   "sms",
		Short: "Run the send-only SMS bridge (Twilio / Vonage)",
		Long: "SMS is outbound-only in rousseau — inbound requires a public HTTP\n" +
			"webhook, which conflicts with the daemon's zero-inbound-surface\n" +
			"posture. Use this transport with the cron scheduler or from other\n" +
			"code paths that need to text a number.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg := opts.Config
			prov := firstNonEmpty(provider, cfg.SMS.Provider)
			fromNum := firstNonEmpty(from, cfg.SMS.From)
			if prov == "" || fromNum == "" {
				return errors.New("sms.provider and sms.from are required")
			}
			setUnattendedPermissionDefault(opts, "sms")

			ctx := cmd.Context()
			wiring, err := assembleDaemon(ctx, opts, nil)
			if err != nil {
				return err
			}
			defer func() { _ = wiring.Sessions.Close() }() //nolint:errcheck // best-effort cleanup

			client, err := sms.New(sms.Config{
				Provider:    sms.Provider(prov),
				From:        fromNum,
				AccountSID:  firstNonEmpty(accountSID, cfg.SMS.AccountSID),
				AuthToken:   firstNonEmpty(authToken, cfg.SMS.AuthToken),
				APIKey:      firstNonEmpty(apiKey, cfg.SMS.APIKey),
				BaseURL:     cfg.SMS.BaseURL,
				ReplyHeader: cfg.SMS.ReplyHeader,
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

			opts.Logger.Info("sms.starting", "provider", prov)
			return client.Start(ctx, wiring.TransportHandler("sms", opts.Logger))
		},
	}
	cmd.Flags().StringVar(&provider, "provider", "", "twilio | vonage")
	cmd.Flags().StringVar(&from, "from", "", "E.164 sending number or Twilio Messaging Service SID")
	cmd.Flags().StringVar(&accountSID, "account-sid", "", "Twilio AccountSID")
	cmd.Flags().StringVar(&authToken, "auth-token", "", "Twilio auth token or Vonage API secret")
	cmd.Flags().StringVar(&apiKey, "api-key", "", "Vonage API key")
	return cmd
}
