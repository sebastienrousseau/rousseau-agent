package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

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
	cmd.AddCommand(newSessionCostCmd(opts))
	return cmd
}

// newSessionCostCmd renders the per-session cost telemetry accumulated
// by [sqlitestore.CostRecorder] over the last `--since` window.
func newSessionCostCmd(opts *Options) *cobra.Command {
	var (
		since   time.Duration
		limit   int
		asJSON  bool
		summary bool
	)
	c := &cobra.Command{
		Use:   "cost [session-id]",
		Short: "Show LLM cost + token counts per session",
		Long: "With a session-id argument, summarise cost for that one session.\n" +
			"Without arguments, list the top-N sessions by cost over the last\n" +
			"--since window (default 7d).",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			base, err := openSessionStore(cmd.Context(), opts)
			if err != nil {
				return err
			}
			defer func() { _ = base.Close() }() //nolint:errcheck // best-effort cleanup

			costStore, err := sqlitestore.NewSessionCostStore(cmd.Context(), base)
			if err != nil {
				return err
			}

			w := cmd.OutOrStdout()
			enc := json.NewEncoder(w)
			enc.SetIndent("", "  ")

			if len(args) == 1 && !summary {
				sum, err := costStore.SumBySession(cmd.Context(), args[0], since)
				if err != nil {
					return err
				}
				if asJSON {
					return enc.Encode(map[string]any{
						"session_id":            args[0],
						"since":                 since.String(),
						"completions":           sum.CompletionCount,
						"input_tokens":          sum.InputTokens,
						"output_tokens":         sum.OutputTokens,
						"cache_read_tokens":     sum.CacheReadTokens,
						"cache_creation_tokens": sum.CacheCreationTokens,
						"cost_usd":              sum.CostUSD,
					})
				}
				_, _ = fmt.Fprintf(w, "session: %s\n", args[0])                         //nolint:errcheck // CLI output
				_, _ = fmt.Fprintf(w, "window:  %s\n", displayWindow(since))            //nolint:errcheck // CLI output
				_, _ = fmt.Fprintf(w, "count:   %d completions\n", sum.CompletionCount) //nolint:errcheck // CLI output
				_, _ = fmt.Fprintf(w, "input:   %d\n", sum.InputTokens)                 //nolint:errcheck // CLI output
				_, _ = fmt.Fprintf(w, "output:  %d\n", sum.OutputTokens)                //nolint:errcheck // CLI output
				_, _ = fmt.Fprintf(w, "cache-r: %d\n", sum.CacheReadTokens)             //nolint:errcheck // CLI output
				_, _ = fmt.Fprintf(w, "cache-c: %d\n", sum.CacheCreationTokens)         //nolint:errcheck // CLI output
				_, _ = fmt.Fprintf(w, "cost:    $%.4f\n", sum.CostUSD)                  //nolint:errcheck // CLI output
				return nil
			}

			top, err := costStore.TopSessions(cmd.Context(), since, limit)
			if err != nil {
				return err
			}
			if asJSON {
				return enc.Encode(map[string]any{
					"since":    since.String(),
					"limit":    limit,
					"sessions": top,
				})
			}
			if len(top) == 0 {
				_, _ = fmt.Fprintln(w, "(no cost data in window)") //nolint:errcheck // CLI output
				return nil
			}
			_, _ = fmt.Fprintf(w, "top %d sessions by cost (window: %s)\n", len(top), displayWindow(since)) //nolint:errcheck // CLI output
			_, _ = fmt.Fprintf(w, "%-10s %10s %8s %8s %8s\n", "session", "cost", "in", "out", "n")          //nolint:errcheck // CLI output
			for _, r := range top {
				_, _ = fmt.Fprintf(w, "%-10s $%9.4f %8d %8d %8d\n", //nolint:errcheck // CLI output
					shortID(r.SessionID), r.CostUSD, r.InputTokens, r.OutputTokens, r.CompletionCount)
			}
			return nil
		},
	}
	c.Flags().DurationVar(&since, "since", 7*24*time.Hour, "aggregate window (0 = all history)")
	c.Flags().IntVar(&limit, "limit", 25, "top-N sessions to list (ignored when a session id is passed)")
	c.Flags().BoolVar(&asJSON, "json", false, "emit machine-readable JSON instead of a table")
	c.Flags().BoolVar(&summary, "summary", false, "with a session-id arg, force the top-N view (ignore the arg)")
	return c
}

func displayWindow(d time.Duration) string {
	if d <= 0 {
		return "all"
	}
	return d.String()
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
			defer func() { _ = store.Close() }() //nolint:errcheck // best-effort cleanup

			hits, err := store.List(cmd.Context(), limit)
			if err != nil {
				return err
			}
			w := cmd.OutOrStdout()
			if len(hits) == 0 {
				fmt.Fprintln(w, "(no sessions)") //nolint:errcheck // CLI output
				return nil
			}
			for _, h := range hits {
				fmt.Fprintf(w, "%s  %-5d  %s  %s\n", shortID(h.ID), h.MessageCount, h.UpdatedAt, h.Title) //nolint:errcheck // CLI output
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
			defer func() { _ = store.Close() }() //nolint:errcheck // best-effort cleanup

			hits, err := store.Search(cmd.Context(), args[0], sqlitestore.SearchOptions{Limit: limit})
			if err != nil {
				return err
			}
			w := cmd.OutOrStdout()
			if len(hits) == 0 {
				fmt.Fprintln(w, "(no matches)") //nolint:errcheck // CLI output
				return nil
			}
			for _, h := range hits {
				fmt.Fprintf(w, "%s  %-40s\n    rank=%.2f  %s\n", shortID(h.SessionID), h.Title, h.Rank, h.Snippet) //nolint:errcheck // CLI output
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
			defer func() { _ = store.Close() }() //nolint:errcheck // best-effort cleanup

			s, err := store.Load(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			w := cmd.OutOrStdout()
			printf := func(f string, a ...any) { _, _ = fmt.Fprintf(w, f, a...) } //nolint:errcheck // CLI output
			printf("id:       %s\ntitle:    %s\ncreated:  %s\nupdated:  %s\nmessages: %d\n\n",
				s.ID, s.Title, s.CreatedAt, s.UpdatedAt, len(s.Messages))
			for i, m := range s.Messages {
				printf("[%d] %s\n", i, m.Role)
				for _, c := range m.Content {
					if c.Text != "" {
						printf("    %s\n", c.Text)
					}
					if c.ToolUse != nil {
						printf("    → %s(%s)\n", c.ToolUse.Name, string(c.ToolUse.Input))
					}
					if c.ToolResult != nil {
						printf("    ← %s\n", c.ToolResult.Output)
					}
				}
				_, _ = fmt.Fprintln(w) //nolint:errcheck // CLI output
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
			defer func() { _ = store.Close() }() //nolint:errcheck // best-effort cleanup
			return store.Delete(cmd.Context(), args[0])
		},
	}
	c.Flags().BoolVar(&confirm, "yes", false, "confirm deletion")
	return c
}

func openSessionStore(ctx context.Context, opts *Options) (*sqlitestore.Store, error) {
	concrete, err := openSQLiteStore(ctx, opts.Config.State)
	if err != nil {
		return nil, err
	}
	return concrete, nil
}

func shortID(s string) string {
	if len(s) < 8 {
		return s
	}
	return s[:8]
}
