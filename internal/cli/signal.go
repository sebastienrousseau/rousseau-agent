package cli

import (
	"context"
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/sebastienrousseau/rousseau-agent/internal/transport/signal"
)

func newSignalCmd(opts *Options) *cobra.Command {
	var (
		account   string
		binary    string
		allowlist []string
	)
	cmd := &cobra.Command{
		Use:   "signal",
		Short: "Run the Signal bridge via signal-cli",
		Long: "Requires signal-cli (https://github.com/AsamK/signal-cli) installed\n" +
			"and pre-registered/linked against the target account. The daemon\n" +
			"invokes `signal-cli -a <account> jsonRpc` and pumps JSON-RPC\n" +
			"traffic between the model and the account.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg := opts.Config
			acct := firstNonEmpty(account, cfg.Signal.Account)
			if acct == "" {
				return errors.New("--account or signal.account is required")
			}
			setUnattendedPermissionDefault(opts, "signal")

			allow := allowlist
			if len(allow) == 0 {
				allow = cfg.Signal.Allowlist
			}

			ctx := cmd.Context()
			wiring, err := assembleDaemon(ctx, opts, allow)
			if err != nil {
				return err
			}
			defer func() { _ = wiring.Sessions.Close() }()

			client, err := signal.New(signal.Config{
				Binary:      firstNonEmpty(binary, cfg.Signal.Binary),
				Account:     acct,
				ExtraArgs:   cfg.Signal.ExtraArgs,
				ReplyHeader: cfg.Signal.ReplyHeader,
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

			opts.Logger.Info("signal.starting", "account", acct, "allowlist", len(allow))
			return client.Start(ctx, wiring.Router)
		},
	}
	cmd.Flags().StringVar(&account, "account", "", "E.164 phone number the daemon runs as")
	cmd.Flags().StringVar(&binary, "binary", "", "path to signal-cli (default: signal-cli on PATH)")
	cmd.Flags().StringSliceVar(&allowlist, "allow", nil, "restrict inbound to these numbers")
	return cmd
}
