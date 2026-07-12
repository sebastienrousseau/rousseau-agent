package cli

import (
	"errors"
	"time"

	"github.com/spf13/cobra"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
	"github.com/sebastienrousseau/rousseau-agent/internal/llm/claudecli"
	sqlitestore "github.com/sebastienrousseau/rousseau-agent/internal/state/sqlite"
	"github.com/sebastienrousseau/rousseau-agent/internal/tools"
	"github.com/sebastienrousseau/rousseau-agent/internal/tools/builtin"
	"github.com/sebastienrousseau/rousseau-agent/internal/transport"
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
			acct := account
			if acct == "" {
				acct = cfg.Signal.Account
			}
			if acct == "" {
				return errors.New("--account or signal.account is required")
			}

			if (cfg.Provider == "" || cfg.Provider == "claudecli") && cfg.ClaudeCLI.PermissionMode == "" {
				cfg.ClaudeCLI.PermissionMode = "bypassPermissions"
				opts.Logger.Warn("signal.permission_mode_default",
					"mode", "bypassPermissions",
					"why", "no claudecli.permission_mode set; unattended daemon cannot approve prompts")
			}
			provider, err := buildProvider(cfg)
			if err != nil {
				return err
			}
			ctx := cmd.Context()

			store, err := openStore(ctx, cfg.State.Path)
			if err != nil {
				return err
			}
			defer func() { _ = store.Close() }()
			concrete := store.(*sqlitestore.Store)
			jidMap, err := sqlitestore.NewJIDMap(ctx, concrete)
			if err != nil {
				return err
			}
			claudeCache, err := sqlitestore.NewClaudeSessionCache(ctx, concrete)
			if err != nil {
				return err
			}
			if cc, ok := provider.(*claudecli.Provider); ok {
				cc.WithCache(claudeCache)
			}

			registry := tools.NewRegistry()
			registry.MustRegister(builtin.NewReadTool())
			registry.MustRegister(builtin.NewWriteTool())
			registry.MustRegister(builtin.NewEditTool())
			registry.MustRegister(builtin.NewGrepTool(0, 0))
			registry.MustRegister(builtin.NewBashTool(60 * time.Second))

			approver, err := buildApprover(cfg.Agent.Approver)
			if err != nil {
				return err
			}
			compressor := buildCompressor(cfg.Agent.Compression, provider)
			ag := agent.New(provider, registry, opts.Logger, agent.Options{
				MaxIterations: cfg.Agent.MaxIterations,
				SystemPrompt:  systemPrompt(cfg.Agent.SystemPrompt),
				Approver:      approver,
				Compressor:    compressor,
			})

			allow := allowlist
			if len(allow) == 0 {
				allow = cfg.Signal.Allowlist
			}
			router := transport.NewRouter(ag, store, jidMap, opts.Logger, transport.RouterOptions{Allowlist: allow})

			client, err := signal.New(signal.Config{
				Binary:      firstNonEmpty(binary, cfg.Signal.Binary),
				Account:     acct,
				ExtraArgs:   cfg.Signal.ExtraArgs,
				ReplyHeader: cfg.Signal.ReplyHeader,
			}, opts.Logger)
			if err != nil {
				return err
			}
			opts.Logger.Info("signal.starting", "account", acct, "allowlist", len(allow))
			return client.Start(ctx, router)
		},
	}
	cmd.Flags().StringVar(&account, "account", "", "E.164 phone number the daemon runs as")
	cmd.Flags().StringVar(&binary, "binary", "", "path to signal-cli (default: signal-cli on PATH)")
	cmd.Flags().StringSliceVar(&allowlist, "allow", nil, "restrict inbound to these numbers")
	return cmd
}
