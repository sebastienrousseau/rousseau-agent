package cli

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/sebastienrousseau/rousseau-agent/internal/config"
	"github.com/sebastienrousseau/rousseau-agent/internal/transport/email"
)

// validateEmailAuth refuses an email allowlist keyed on a forgeable
// From header: anyone could then act as an allowlisted sender, tools
// included. The gate needs our MTA's authserv-id, since senders can
// write Authentication-Results headers too.
func validateEmailAuth(cfg config.EmailConfig, allow []string, allowAnyone bool, logger *slog.Logger) error {
	switch {
	case cfg.RequireAuthenticationResults && strings.TrimSpace(cfg.TrustedAuthservID) == "":
		return errors.New("email.trusted_authserv_id is required with email.require_authentication_results: set it to the authserv-id your MTA writes in Authentication-Results")
	case cfg.RequireAuthenticationResults, allowAnyone || len(allow) == 0:
		return nil
	case cfg.InsecureTrustFrom:
		logger.Warn("email.allowlist_unauthenticated",
			slog.String("effect", "the From header is trivially forged, so anyone can act as an allowlisted sender"),
			slog.String("fix", "set email.require_authentication_results and email.trusted_authserv_id"))
		return nil
	default:
		return errors.New("email allowlist keys on the From header, which anyone can forge: set email.require_authentication_results: true and email.trusted_authserv_id, or email.insecure_trust_from: true for a test inbox")
	}
}

// checkEmailSenders applies the sender policy every chat transport
// has, plus the email-specific rule that an allowlist must rest on
// authenticated mail.
func checkEmailSenders(cfg config.EmailConfig, allow []string, opts *Options) error {
	if err := validateEmailAuth(cfg, allow, opts.AllowAnyone, opts.Logger); err != nil {
		return needsOperator(err)
	}
	return requireSenderPolicy("email", allow, opts.AllowAnyone)
}

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
		allow        []string
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
			if err := requirePermissionMode(opts, "email"); err != nil {
				return err
			}

			if len(allow) == 0 {
				allow = cfg.Email.Allowlist
			}
			allow = normalizeEmailAllowlist(allow)
			if err := checkEmailSenders(cfg.Email, allow, opts); err != nil {
				return err
			}

			ctx := cmd.Context()
			wiring, err := assembleDaemon(ctx, opts, allow)
			if err != nil {
				return err
			}
			defer func() { _ = wiring.Cleanup() }() //nolint:errcheck // best-effort: closes MCP clients, flushes audit (daemon.stop), then the store
			wiring.StartBackgroundServers(ctx)
			startHeartbeat(ctx, opts, "email", nil)

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

				RequireAuthResults: cfg.Email.RequireAuthenticationResults,
				TrustedAuthservID:  cfg.Email.TrustedAuthservID,
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
			return client.Start(ctx, wiring.TransportHandler("email", opts.Logger))
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
	cmd.Flags().StringSliceVar(&allow, "allow", nil, "sender addresses allowed to reach the agent (repeatable, case-insensitive); falls back to email.allowlist")
	return cmd
}
