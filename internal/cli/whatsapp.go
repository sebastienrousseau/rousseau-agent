package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/sebastienrousseau/rousseau-agent/internal/transport/whatsapp"
)

func newWhatsAppCmd(opts *Options) *cobra.Command {
	var (
		storePath string
		allowlist []string
	)
	cmd := &cobra.Command{
		Use:   "whatsapp",
		Short: "Run the WhatsApp bridge (unofficial WhatsApp Web protocol)",
		Long: "Run the WhatsApp bridge in the foreground.\n\n" +
			"On first launch a QR code is printed to stdout — scan it from your phone in\n" +
			"WhatsApp > Settings > Linked devices. Device credentials are cached locally,\n" +
			"subsequent launches connect silently.\n\n" +
			"Uses the UNOFFICIAL WhatsApp Web protocol (whatsmeow). Meta occasionally bans\n" +
			"numbers using unofficial clients — do not run this on a number you rely on.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			setUnattendedPermissionDefault(opts, "whatsapp")
			ctx := cmd.Context()

			wiring, err := assembleDaemon(ctx, opts, allowlist)
			if err != nil {
				return err
			}
			defer func() { _ = wiring.Sessions.Close() }() //nolint:errcheck // best-effort cleanup

			dsn, err := resolveWhatsAppDSN(storePath)
			if err != nil {
				return err
			}

			client, err := whatsapp.New(whatsapp.Config{
				StoreDSN:    dsn,
				LogLevel:    whatsappLogLevel(opts.Config.Log.Level),
				ReplyHeader: opts.Config.WhatsApp.ReplyHeader,
				Transcriber: buildTranscriber(opts),
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

			opts.Logger.Info("whatsapp.starting", "store", dsn, "allowlist", len(allowlist))
			return client.Start(ctx, wiring.Router)
		},
	}
	cmd.Flags().StringVar(&storePath, "store", "", "path to whatsmeow device store (default: $XDG_DATA_HOME/rousseau/whatsapp.db)")
	cmd.Flags().StringSliceVar(&allowlist, "allow", nil, "restrict inbound to these JIDs (repeatable). Empty allows anyone — do not do this on a public number.")
	return cmd
}

// buildTranscriber constructs a whisper-backed transcriber from config,
// or returns nil when voice notes are disabled.
func buildTranscriber(opts *Options) whatsapp.Transcriber {
	v := opts.Config.WhatsApp.Voice
	if !v.Enabled {
		return nil
	}
	opts.Logger.Info("whatsapp.voice_enabled",
		"binary", firstNonEmpty(v.Binary, "whisper"),
		"model", firstNonEmpty(v.Model, v.ModelPath))
	return whatsapp.NewWhisperTranscriber(whatsapp.WhisperConfig{
		Binary:    v.Binary,
		Model:     v.Model,
		ModelPath: v.ModelPath,
		Language:  v.Language,
		ExtraArgs: v.ExtraArgs,
	})
}

func resolveWhatsAppDSN(path string) (string, error) {
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home: %w", err)
		}
		path = filepath.Join(home, ".local", "share", "rousseau", "whatsapp.db")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", fmt.Errorf("create whatsapp store dir: %w", err)
	}
	// modernc.org/sqlite DSN pragmas explained in git history; see
	// commit 55fdee3 (SQLITE_BUSY fix) for the load-bearing rationale.
	return "file:" + path + "?_pragma=foreign_keys(1)" +
		"&_pragma=journal_mode(WAL)" +
		"&_pragma=busy_timeout(15000)" +
		"&_pragma=synchronous(NORMAL)", nil
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}

func whatsappLogLevel(level string) string {
	switch level {
	case "debug":
		return "DEBUG"
	case "warn", "warning":
		return "WARN"
	case "error":
		return "ERROR"
	default:
		return "INFO"
	}
}
