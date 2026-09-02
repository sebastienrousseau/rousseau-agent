package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
	"github.com/sebastienrousseau/rousseau-agent/internal/agent/subagent"
	"github.com/sebastienrousseau/rousseau-agent/internal/config"
	"github.com/sebastienrousseau/rousseau-agent/internal/state"
	pgstore "github.com/sebastienrousseau/rousseau-agent/internal/state/postgres"
	sqlitestore "github.com/sebastienrousseau/rousseau-agent/internal/state/sqlite"
	"github.com/sebastienrousseau/rousseau-agent/internal/tools"
	"github.com/sebastienrousseau/rousseau-agent/internal/tools/builtin"
	"github.com/sebastienrousseau/rousseau-agent/internal/tui"
)

func newChatCmd(opts *Options) *cobra.Command {
	var (
		sessionID string
		title     string
	)
	cmd := &cobra.Command{
		Use:   "chat",
		Short: "Open the interactive Bubble Tea chat",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg := opts.Config
			provider, err := buildProvider(cfg)
			if err != nil {
				return err
			}

			ctx := cmd.Context()
			store, err := openStore(ctx, cfg.State)
			if err != nil {
				return err
			}
			defer func() { _ = store.Close() }() //nolint:errcheck // best-effort cleanup

			registry := tools.NewRegistry()
			registry.MustRegister(builtin.NewReadTool())
			registry.MustRegister(builtin.NewWriteTool())
			registry.MustRegister(builtin.NewEditTool())
			registry.MustRegister(builtin.NewGrepTool(0, 0))
			bash, err := buildBashTool(cfg.Tools.Bash)
			if err != nil {
				return fmt.Errorf("cli: build bash tool: %w", err)
			}
			registry.MustRegister(bash)
			// spawn_subagent — see daemon.go for the policy rationale.
			registry.MustRegister(builtin.NewSpawnSubagentTool(subagent.Policy{}))

			// Interactive-approver default for `chat`: the user IS
			// present, so per-tool prompts are the right UX. The
			// remembered "always allow / deny" answers within a
			// session make it non-annoying after the first prompt per
			// tool. Config can still supply a PatternApprover — when
			// present it wraps the interactive approver so
			// pre-approved calls skip the prompt entirely.
			interactive := tui.NewApprover()
			configured, err := buildApprover(cfg.Agent.Approver)
			if err != nil {
				return err
			}
			approver := chainApprovers(configured, interactive)

			compressor := buildCompressor(cfg.Agent.Compression, provider)
			ag := agent.New(provider, registry, opts.Logger, agent.Options{
				MaxIterations: cfg.Agent.MaxIterations,
				SystemPrompt:  systemPrompt(cfg.Agent.SystemPrompt),
				Approver:      approver,
				Compressor:    compressor,
			})

			session, err := loadOrCreateSession(ctx, store, sessionID, title)
			if err != nil {
				return err
			}

			model := tui.New(ag, store, opts.Logger, session)
			program := tea.NewProgram(model, tea.WithAltScreen(), tea.WithContext(ctx))
			// Bind the approver to the program's Send after the
			// program exists but before Run — Send is safe on any
			// goroutine and messages are buffered until Run starts
			// draining. Doing it here (not in NewApprover) avoids the
			// chicken-and-egg between approver-goes-to-agent and
			// program-needs-model.
			interactive.Bind(program.Send)
			_, err = program.Run()
			return err
		},
	}
	cmd.Flags().StringVar(&sessionID, "session", "", "resume an existing session by id")
	cmd.Flags().StringVar(&title, "title", "", "title for a new session")
	return cmd
}

// openStore dispatches to the driver named in cfg.Driver. Empty
// driver defaults to sqlite so existing single-replica installs
// keep working with no config change. Postgres requires cfg.DSN;
// misconfiguration is surfaced at Open time (fail-fast) rather
// than deferred to the first Save.
func openStore(ctx context.Context, cfg config.StateConfig) (state.Store, error) {
	driver := cfg.Driver
	if driver == "" {
		driver = "sqlite"
	}
	switch driver {
	case "sqlite":
		path := cfg.Path
		if path == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return nil, fmt.Errorf("resolve home: %w", err)
			}
			path = filepath.Join(home, ".local", "share", "rousseau", "sessions.db")
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, fmt.Errorf("create state dir: %w", err)
		}
		return sqlitestore.Open(ctx, path)
	case "postgres":
		if cfg.DSN == "" {
			return nil, fmt.Errorf("state driver=postgres requires state.dsn (e.g. postgres://user:pass@host:5432/db?sslmode=require)")
		}
		return pgstore.Open(ctx, cfg.DSN)
	default:
		return nil, fmt.Errorf("unknown state driver %q (want \"sqlite\" or \"postgres\")", driver)
	}
}

// openSQLiteStore opens a store for the commands whose extension
// callsites still consume the sqlite driver's concrete types
// (cron, jidmap, oauth, session_cache, session_costs) even though
// Postgres implementations of every one of those tables exist in
// internal/state/postgres. Errors cleanly when the operator has
// configured a non-sqlite driver — the alternative (silent
// fall-through to sqlite) would silently degrade an HA deploy back
// to per-replica state.
//
// Next step to remove this seam: teach each extension callsite
// to hold either concrete type (the two drivers share method
// signatures + type-alias the domain shapes so this is a wiring
// change, not a redesign). Recall stays sqlite-only until FTS5 →
// tsvector is designed separately.
func openSQLiteStore(ctx context.Context, cfg config.StateConfig) (*sqlitestore.Store, error) {
	driver := cfg.Driver
	if driver == "" {
		driver = "sqlite"
	}
	if driver != "sqlite" {
		return nil, fmt.Errorf("this command still holds the sqlite driver's concrete types for its extension tables (cron/jidmap/oauth/session_cache/session_costs) — Postgres implementations of those tables exist but are not yet wired here; recall stays sqlite-only. Configure state.driver=sqlite (got %q)", driver)
	}
	store, err := openStore(ctx, cfg)
	if err != nil {
		return nil, err
	}
	concrete, ok := store.(*sqlitestore.Store)
	if !ok {
		_ = store.Close() //nolint:errcheck // best-effort cleanup on error path
		return nil, errors.New("openSQLiteStore: driver=sqlite did not return *sqlite.Store — refusing to proceed")
	}
	return concrete, nil
}

func loadOrCreateSession(ctx context.Context, store state.Store, id, title string) (*agent.Session, error) {
	if id != "" {
		sess, err := store.Load(ctx, id)
		if err != nil {
			return nil, err
		}
		return sess, nil
	}
	if title == "" {
		title = "chat " + time.Now().UTC().Format("2006-01-02 15:04")
	}
	sess := agent.NewSession(title)
	if err := store.Save(ctx, sess); err != nil {
		return nil, err
	}
	return sess, nil
}

func systemPrompt(override string) string {
	if override != "" {
		return override
	}
	return `You are rousseau, a careful, concise coding assistant running in a terminal.
When you need to inspect the filesystem or run commands, prefer the smallest tool that answers the question.
Never fabricate file contents; use the read tool. Never invent shell output; use the bash tool.
When you finish a turn, summarise what changed and what the user should verify.`
}
