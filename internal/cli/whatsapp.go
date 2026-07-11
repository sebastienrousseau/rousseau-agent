package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
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
			// A daemon has no interactive terminal for claude to prompt
			// against. If the operator hasn't picked a permission mode,
			// pick one that lets tool calls actually complete and log a
			// prominent warning about the tradeoff.
			if (cfg.Provider == "" || cfg.Provider == "claudecli") && cfg.ClaudeCLI.PermissionMode == "" {
				cfg.ClaudeCLI.PermissionMode = "bypassPermissions"
				opts.Logger.Warn("whatsapp.permission_mode_default",
					"mode", "bypassPermissions",
					"why", "no claudecli.permission_mode set; unattended daemon cannot approve prompts",
					"how_to_override", "set claudecli.permission_mode in ~/.config/rousseau/config.yaml (acceptEdits is a narrower alternative)",
				)
			}
			provider, err := buildProvider(cfg)
			if err != nil {
				return err
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
				StoreDSN:    dsn,
				LogLevel:    whatsappLogLevel(cfg.Log.Level),
				ReplyHeader: cfg.WhatsApp.ReplyHeader,
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
	// modernc.org/sqlite DSN. Notes on each pragma:
	//   foreign_keys=ON      — whatsmeow's schema uses FK constraints.
	//   journal_mode=WAL     — readers and writers coexist without blocking.
	//   busy_timeout=15000   — wait up to 15s on lock contention. Without
	//                          this, whatsmeow's concurrent writes during
	//                          initial history-sync race and one loses with
	//                          SQLITE_BUSY, which cascades into failed
	//                          session-identity saves and dropped inbound
	//                          message decryption.
	//   synchronous=NORMAL   — safe with WAL, reduces fsync churn under load.
	return "file:" + path + "?_pragma=foreign_keys(1)" +
		"&_pragma=journal_mode(WAL)" +
		"&_pragma=busy_timeout(15000)" +
		"&_pragma=synchronous(NORMAL)", nil
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
