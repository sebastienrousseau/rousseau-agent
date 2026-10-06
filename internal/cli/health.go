package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/sebastienrousseau/rousseau-agent/internal/health"
	"github.com/sebastienrousseau/rousseau-agent/internal/observability"
)

const (
	heartbeatInterval = 15 * time.Second
	heartbeatMaxAge   = 90 * time.Second
)

// rousseauDataDir is $XDG_DATA_HOME/rousseau, or ~/.local/share/rousseau.
func rousseauDataDir() (string, error) {
	if d := os.Getenv("XDG_DATA_HOME"); d != "" {
		return filepath.Join(d, "rousseau"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home: %w", err)
	}
	return filepath.Join(home, ".local", "share", "rousseau"), nil
}

// startHeartbeat keeps the transport's heartbeat file fresh for
// `rousseau health` until ctx ends. conn may be nil for transports
// with no connection notion. Failure to resolve the data dir only
// logs: the health check then reports "no heartbeat", which is the
// right signal.
func startHeartbeat(ctx context.Context, opts *Options, transportName string, conn health.ConnectedFunc) {
	// The HTTP readiness probe (/readyz on the metrics listener)
	// reads the same link state the heartbeat records.
	observability.SetReadinessCheck(func() error {
		if conn == nil {
			return nil
		}
		connected, ok := conn()
		if ok && !connected {
			return fmt.Errorf("%s transport is not connected", transportName)
		}
		return nil
	})
	dir, err := rousseauDataDir()
	if err != nil {
		opts.Logger.Warn("health.heartbeat_disabled", "err", err.Error())
		return
	}
	go health.Run(ctx, health.Path(dir, transportName), transportName, conn, heartbeatInterval)
}

// newHealthCmd is the container HealthCmd: exit 0 only when the named
// transport's daemon is beating and, where it has a link, connected.
// It skips config loading so a broken config cannot mask a dead
// daemon.
func newHealthCmd() *cobra.Command {
	var maxAge time.Duration
	cmd := &cobra.Command{
		Use:   "health <transport>",
		Short: "Exit 0 if the transport daemon is alive and connected (for container health checks)",
		Long: "Reads the heartbeat the running daemon writes every " + heartbeatInterval.String() + " to\n" +
			"$XDG_DATA_HOME/rousseau/health/<transport>.json and fails when it is missing,\n" +
			"older than --max-age, or reports the transport disconnected.",
		Args:              cobra.ExactArgs(1),
		PersistentPreRunE: func(*cobra.Command, []string) error { return nil },
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := rousseauDataDir()
			if err != nil {
				return err
			}
			if err := health.Check(health.Path(dir, args[0]), maxAge, time.Now()); err != nil {
				return fmt.Errorf("%s: unhealthy: %w", args[0], err)
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s: healthy\n", args[0])
			return err
		},
	}
	cmd.Flags().DurationVar(&maxAge, "max-age", heartbeatMaxAge, "fail when the last heartbeat is older than this")
	return cmd
}
