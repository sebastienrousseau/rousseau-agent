package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
	"github.com/sebastienrousseau/rousseau-agent/internal/llm/anthropic"
	sqlitestore "github.com/sebastienrousseau/rousseau-agent/internal/state/sqlite"
	"github.com/sebastienrousseau/rousseau-agent/internal/tools"
	"github.com/sebastienrousseau/rousseau-agent/internal/tools/builtin"
	"github.com/sebastienrousseau/rousseau-agent/internal/transport"
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
			cfg := opts.Config
			if cfg.Anthropic.APIKey == "" {
				return errors.New("missing ANTHROPIC_API_KEY (set env var or anthropic.api_key in config)")
			}
			ctx := cmd.Context()

			sessionsStore, err := openStore(ctx, cfg.State.Path)
			if err != nil {
				return err
			}
			defer func() { _ = sessionsStore.Close() }()

			jidMap, err := sqlitestore.NewJIDMap(ctx, sessionsStore.(*sqlitestore.Store))
			if err != nil {
				return err
			}

			provider, err := anthropic.New(anthropic.Config{
				APIKey:    cfg.Anthropic.APIKey,
				Model:     cfg.Anthropic.Model,
				MaxTokens: cfg.Anthropic.MaxTokens,
			})
			if err != nil {
				return err
			}

			registry := tools.NewRegistry()
			registry.MustRegister(builtin.NewReadTool())
			registry.MustRegister(builtin.NewWriteTool())
			registry.MustRegister(builtin.NewEditTool())
			registry.MustRegister(builtin.NewGrepTool(0, 0))
			registry.MustRegister(builtin.NewBashTool(60 * time.Second))

			ag := agent.New(provider, registry, opts.Logger, agent.Options{
				MaxIterations: cfg.Agent.MaxIterations,
				SystemPrompt:  systemPrompt(cfg.Agent.SystemPrompt),
			})

			router := transport.NewRouter(ag, sessionsStore, jidMap, opts.Logger, transport.RouterOptions{
				Allowlist: allowlist,
			})

			dsn, err := resolveWhatsAppDSN(storePath)
			if err != nil {
				return err
			}
			client, err := whatsapp.New(whatsapp.Config{
				StoreDSN: dsn,
				LogLevel: whatsappLogLevel(cfg.Log.Level),
			}, opts.Logger)
			if err != nil {
				return err
			}

			opts.Logger.Info("whatsapp.starting", "store", dsn, "allowlist", len(allowlist))
			return client.Start(ctx, router)
		},
	}
	cmd.Flags().StringVar(&storePath, "store", "", "path to whatsmeow device store (default: $XDG_DATA_HOME/rousseau/whatsapp.db)")
	cmd.Flags().StringSliceVar(&allowlist, "allow", nil, "restrict inbound to these JIDs (repeatable). Empty allows anyone — do not do this on a public number.")
	return cmd
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
	// modernc.org/sqlite DSN — file mode with foreign keys and WAL.
	return "file:" + path + "?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)", nil
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
