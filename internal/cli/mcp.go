package cli

import (
	"os"

	"github.com/spf13/cobra"

	"github.com/sebastienrousseau/rousseau-agent/internal/mcp"
	sqlitestore "github.com/sebastienrousseau/rousseau-agent/internal/state/sqlite"
)

func newMCPCmd(opts *Options) *cobra.Command {
	return &cobra.Command{
		Use:   "mcp",
		Short: "Start a stdio Model Context Protocol server exposing rousseau state",
		Long: "Publishes rousseau's session store and cron jobs as MCP tools\n" +
			"(rousseau_search_sessions, rousseau_list_sessions,\n" +
			"rousseau_read_session, rousseau_cron_list) over stdin/stdout.\n\n" +
			"Register in Claude Code, Cursor, or any MCP host as a\n" +
			"stdio server pointing at `rousseau mcp`.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			store, err := openStore(ctx, opts.Config.State.Path)
			if err != nil {
				return err
			}
			defer func() { _ = store.Close() }()
			concrete := store.(*sqlitestore.Store)

			cronStore, err := sqlitestore.NewCronStore(ctx, concrete)
			if err != nil {
				return err
			}

			s := mcp.NewServer("rousseau", version, opts.Logger)
			mcp.RegisterRousseauTools(s, mcp.NewStoreBackend(concrete, cronStore))
			return s.Serve(ctx, os.Stdin, os.Stdout)
		},
	}
}
