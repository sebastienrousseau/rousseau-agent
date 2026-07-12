package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
	"github.com/sebastienrousseau/rousseau-agent/internal/state"
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
			store, err := openStore(ctx, cfg.State.Path)
			if err != nil {
				return err
			}
			defer func() { _ = store.Close() }() //nolint:errcheck // best-effort cleanup

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

			session, err := loadOrCreateSession(ctx, store, sessionID, title)
			if err != nil {
				return err
			}

			model := tui.New(ag, store, opts.Logger, session)
			program := tea.NewProgram(model, tea.WithAltScreen(), tea.WithContext(ctx))
			_, err = program.Run()
			return err
		},
	}
	cmd.Flags().StringVar(&sessionID, "session", "", "resume an existing session by id")
	cmd.Flags().StringVar(&title, "title", "", "title for a new session")
	return cmd
}

func openStore(ctx context.Context, path string) (state.Store, error) {
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
