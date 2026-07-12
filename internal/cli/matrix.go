package cli

import (
	"context"
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/sebastienrousseau/rousseau-agent/internal/transport/matrix"
)

func newMatrixCmd(opts *Options) *cobra.Command {
	var (
		homeserver string
		token      string
		userID     string
		allowlist  []string
	)
	cmd := &cobra.Command{
		Use:   "matrix",
		Short: "Run the Matrix client-server bridge",
		Long: "Requires a Matrix bot user with an access token. The daemon\n" +
			"long-polls /sync and routes each incoming m.room.message from a\n" +
			"non-self sender through the agent. Reply lands via\n" +
			"/rooms/{room}/send/m.room.message.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg := opts.Config
			hs := firstNonEmpty(homeserver, cfg.Matrix.HomeserverURL)
			tok := firstNonEmpty(token, cfg.Matrix.AccessToken)
			if hs == "" || tok == "" {
				return errors.New("matrix.homeserver_url and matrix.access_token are required")
			}
			uid := firstNonEmpty(userID, cfg.Matrix.UserID)
			setUnattendedPermissionDefault(opts, "matrix")

			allow := allowlist
			if len(allow) == 0 {
				allow = cfg.Matrix.Allowlist
			}

			ctx := cmd.Context()
			wiring, err := assembleDaemon(ctx, opts, allow)
			if err != nil {
				return err
			}
			defer func() { _ = wiring.Sessions.Close() }()

			client, err := matrix.New(matrix.Config{
				HomeserverURL: hs,
				AccessToken:   tok,
				UserID:        uid,
				ReplyHeader:   cfg.Matrix.ReplyHeader,
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

			opts.Logger.Info("matrix.starting", "homeserver", hs, "allowlist", len(allow))
			return client.Start(ctx, wiring.Router)
		},
	}
	cmd.Flags().StringVar(&homeserver, "homeserver", "", "homeserver base URL (e.g. https://matrix.org)")
	cmd.Flags().StringVar(&token, "token", "", "bot access token")
	cmd.Flags().StringVar(&userID, "user-id", "", "bot MXID for own-message loop prevention")
	cmd.Flags().StringSliceVar(&allowlist, "allow", nil, "restrict inbound to these MXIDs")
	return cmd
}
