package cli

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/sebastienrousseau/rousseau-agent/internal/transport/email"
)

func newEmailCmd(opts *Options) *cobra.Command {
	var (
		imapAddr     string
		imapUsername string
		imapPassword string
		smtpAddr     string
		smtpUsername string
		smtpPassword string
		from         string
		mailbox      string
		pollInterval string
	)
	cmd := &cobra.Command{
		Use:   "email",
		Short: "Run the email bridge over IMAP (inbound) + SMTP (outbound)",
		Long: "Polls IMAP UNSEEN mail and replies via SMTP. IMAP IDLE is not\n" +
			"yet supported; poll cadence defaults to 30s. All connections use\n" +
			"TLS; STARTTLS-only servers are not currently supported.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg := opts.Config
			im := firstNonEmpty(imapAddr, cfg.Email.IMAPAddr)
			imUser := firstNonEmpty(imapUsername, cfg.Email.IMAPUsername)
			imPass := firstNonEmpty(imapPassword, cfg.Email.IMAPPassword)
			sm := firstNonEmpty(smtpAddr, cfg.Email.SMTPAddr)
			smUser := firstNonEmpty(smtpUsername, cfg.Email.SMTPUsername)
			smPass := firstNonEmpty(smtpPassword, cfg.Email.SMTPPassword)
			fromAddr := firstNonEmpty(from, cfg.Email.From)
			if im == "" || imUser == "" || imPass == "" {
				return errors.New("email: IMAP settings are required")
			}
			if sm == "" || smUser == "" || smPass == "" {
				return errors.New("email: SMTP settings are required")
			}
			if fromAddr == "" {
				return errors.New("email.from is required")
			}
			setUnattendedPermissionDefault(opts, "email")

			ctx := cmd.Context()
			wiring, err := assembleDaemon(ctx, opts, nil)
			if err != nil {
				return err
			}
			defer func() { _ = wiring.Sessions.Close() }()

			poll := 0 * time.Second
			if s := firstNonEmpty(pollInterval, cfg.Email.PollInterval); s != "" {
				d, err := time.ParseDuration(s)
				if err != nil {
					return fmt.Errorf("email: poll_interval: %w", err)
				}
				poll = d
			}

			client, err := email.New(email.Config{
				IMAPAddr:     im,
				IMAPUsername: imUser,
				IMAPPassword: imPass,
				Mailbox:      firstNonEmpty(mailbox, cfg.Email.Mailbox),
				PollInterval: poll,

				SMTPAddr:     sm,
				SMTPUsername: smUser,
				SMTPPassword: smPass,

				From:        fromAddr,
				ReplyHeader: cfg.Email.ReplyHeader,
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

			opts.Logger.Info("email.starting", "imap", im, "smtp", sm)
			return client.Start(ctx, wiring.Router)
		},
	}
	cmd.Flags().StringVar(&imapAddr, "imap-addr", "", "imap.example.com:993")
	cmd.Flags().StringVar(&imapUsername, "imap-username", "", "IMAP username")
	cmd.Flags().StringVar(&imapPassword, "imap-password", "", "IMAP password")
	cmd.Flags().StringVar(&smtpAddr, "smtp-addr", "", "smtp.example.com:587")
	cmd.Flags().StringVar(&smtpUsername, "smtp-username", "", "SMTP username")
	cmd.Flags().StringVar(&smtpPassword, "smtp-password", "", "SMTP password")
	cmd.Flags().StringVar(&from, "from", "", "From: address")
	cmd.Flags().StringVar(&mailbox, "mailbox", "", "IMAP mailbox (defaults to INBOX)")
	cmd.Flags().StringVar(&pollInterval, "poll-interval", "", "polling cadence, e.g. 30s")
	return cmd
}
