package cli

import (
	"context"
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	sqlitestore "github.com/sebastienrousseau/rousseau-agent/internal/state/sqlite"
)

func newSessionCmd(opts *Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "session",
		Short: "Inspect and search stored conversations",
	}
	cmd.AddCommand(newSessionListCmd(opts))
	cmd.AddCommand(newSessionSearchCmd(opts))
	cmd.AddCommand(newSessionShowCmd(opts))
	cmd.AddCommand(newSessionDeleteCmd(opts))
	return cmd
}

func newSessionListCmd(opts *Options) *cobra.Command {
	var limit int
	c := &cobra.Command{
		Use:   "list",
		Short: "List recent sessions newest-first",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			store, err := openSessionStore(cmd.Context(), opts)
			if err != nil {
				return err
			}
			defer func() { _ = store.Close() }()

			hits, err := store.List(cmd.Context(), limit)
			if err != nil {
				return err
			}
			w := cmd.OutOrStdout()
			if len(hits) == 0 {
				fmt.Fprintln(w, "(no sessions)")
				return nil
			}
			for _, h := range hits {
				fmt.Fprintf(w, "%s  %-5d  %s  %s\n", shortID(h.ID), h.MessageCount, h.UpdatedAt, h.Title)
			}
			return nil
		},
	}
	c.Flags().IntVar(&limit, "limit", 20, "cap on rows returned (0 = unlimited)")
	return c
}

func newSessionSearchCmd(opts *Options) *cobra.Command {
	var limit int
	c := &cobra.Command{
		Use:   "search <query>",
		Short: "Full-text search across session history",
		Long: "Runs an FTS5 query against every recorded conversation. Uses\n" +
			"SQLite FTS5 syntax: phrases go in double quotes, operators are\n" +
			"AND/OR/NOT, prefix search with 'kub*'.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := openSessionStore(cmd.Context(), opts)
			if err != nil {
				return err
			}
			defer func() { _ = store.Close() }()

			hits, err := store.Search(cmd.Context(), args[0], sqlitestore.SearchOptions{Limit: limit})
			if err != nil {
				return err
			}
			w := cmd.OutOrStdout()
			if len(hits) == 0 {
				fmt.Fprintln(w, "(no matches)")
				return nil
			}
			for _, h := range hits {
				fmt.Fprintf(w, "%s  %-40s\n    rank=%.2f  %s\n", shortID(h.SessionID), h.Title, h.Rank, h.Snippet)
			}
			return nil
		},
	}
	c.Flags().IntVar(&limit, "limit", 20, "cap on hits returned")
	return c
}

func newSessionShowCmd(opts *Options) *cobra.Command {
	return &cobra.Command{
		Use:   "show <session-id>",
		Short: "Print the full transcript of a session",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := openSessionStore(cmd.Context(), opts)
			if err != nil {
				return err
			}
			defer func() { _ = store.Close() }()

			s, err := store.Load(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			w := cmd.OutOrStdout()
			fmt.Fprintf(w, "id:       %s\ntitle:    %s\ncreated:  %s\nupdated:  %s\nmessages: %d\n\n",
				s.ID, s.Title, s.CreatedAt, s.UpdatedAt, len(s.Messages))
			for i, m := range s.Messages {
				fmt.Fprintf(w, "[%d] %s\n", i, m.Role)
				for _, c := range m.Content {
					if c.Text != "" {
						fmt.Fprintf(w, "    %s\n", c.Text)
					}
					if c.ToolUse != nil {
						fmt.Fprintf(w, "    → %s(%s)\n", c.ToolUse.Name, string(c.ToolUse.Input))
					}
					if c.ToolResult != nil {
						fmt.Fprintf(w, "    ← %s\n", c.ToolResult.Output)
					}
				}
				fmt.Fprintln(w)
			}
			return nil
		},
	}
}

func newSessionDeleteCmd(opts *Options) *cobra.Command {
	var confirm bool
	c := &cobra.Command{
		Use:   "delete <session-id>",
		Short: "Delete a session by id",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !confirm {
				return errors.New("refusing to delete without --yes")
			}
			store, err := openSessionStore(cmd.Context(), opts)
			if err != nil {
				return err
			}
			defer func() { _ = store.Close() }()
			return store.Delete(cmd.Context(), args[0])
		},
	}
	c.Flags().BoolVar(&confirm, "yes", false, "confirm deletion")
	return c
}

func openSessionStore(ctx context.Context, opts *Options) (*sqlitestore.Store, error) {
	store, err := openStore(ctx, opts.Config.State.Path)
	if err != nil {
		return nil, err
	}
	concrete, ok := store.(*sqlitestore.Store)
	if !ok {
		_ = store.Close()
		return nil, errors.New("session commands require the sqlite store")
	}
	return concrete, nil
}

func shortID(s string) string {
	if len(s) < 8 {
		return s
	}
	return s[:8]
}
