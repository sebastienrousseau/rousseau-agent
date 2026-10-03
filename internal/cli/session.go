package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/sebastienrousseau/rousseau-agent/internal/llm/claudecli"
	"github.com/sebastienrousseau/rousseau-agent/internal/senderkey"

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
	cmd.AddCommand(newSessionDeleteBySenderCmd(opts))
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
			base, err := openSearchableStore(cmd.Context(), opts.Config.State)
			if err != nil {
				return err
			}
			defer func() { _ = base.Close() }() //nolint:errcheck // best-effort cleanup

			costStore, err := openSessionCostStore(cmd.Context(), base)
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
			store, err := openSearchableStore(cmd.Context(), opts.Config.State)
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
			store, err := openSearchableStore(cmd.Context(), opts.Config.State)
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
			store, err := openSearchableStore(cmd.Context(), opts.Config.State)
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
			store, err := openSearchableStore(cmd.Context(), opts.Config.State)
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

func shortID(s string) string {
	if len(s) < 8 {
		return s
	}
	return s[:8]
}

// senderEraser is implemented by stores that support GDPR erasure.
type senderEraser interface {
	EraseSender(ctx context.Context, sender string) (sqlitestore.EraseReport, error)
}

// newSessionDeleteBySenderCmd implements GDPR Article 17 erasure for
// one sender: everything the session store holds for them (see
// sqlite.Store.EraseSender) plus claude's own transcripts of their
// sessions.
func newSessionDeleteBySenderCmd(opts *Options) *cobra.Command {
	var confirm bool
	c := &cobra.Command{
		Use:   "delete-by-sender <sender>",
		Short: "Erase everything stored for one sender (GDPR Art. 17)",
		Long: "Deletes the sender's sessions (and search index rows), jid mapping, identity\n" +
			"handles, SSO bindings, cron jobs delivering to them, per-session costs, recall\n" +
			"vectors and reliability samples, with secure_delete and a WAL checkpoint, then\n" +
			"removes claude's transcripts of those sessions. The sender is the stored key,\n" +
			"e.g. whatsapp:15551234567@s.whatsapp.net, or the bare identifier when only one\n" +
			"transport holds it (it is refused when several do). Stop the daemon first so it does\n" +
			"not recreate a session mid-erasure. Not covered: the WhatsApp device store\n" +
			"(whatsapp.db), sessions saved before sender tracking, and backups.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !confirm {
				return errors.New("refusing to erase without --yes")
			}
			store, err := openSearchableStore(cmd.Context(), opts.Config.State)
			if err != nil {
				return err
			}
			defer func() { _ = store.Close() }() //nolint:errcheck // best-effort cleanup
			eraser, ok := store.(senderEraser)
			if !ok {
				return fmt.Errorf("erasure is not implemented for the %q state driver yet", driverName(opts.Config.State))
			}
			key, err := resolveSenderKey(cmd, store, args[0])
			if err != nil {
				return err
			}
			rep, err := eraser.EraseSender(cmd.Context(), key)
			if err != nil {
				return err
			}
			files, err := claudecli.EraseTranscripts(rep.SessionIDs)
			if err != nil {
				return fmt.Errorf("store erased, but claude transcripts were not: %w", err)
			}
			out := cmd.OutOrStdout()
			_, _ = fmt.Fprintf(out, "erased %s: %d session(s), %d claude transcript file(s)\n", key, len(rep.SessionIDs), files) //nolint:errcheck // CLI output
			tables := make([]string, 0, len(rep.Rows))
			for t := range rep.Rows {
				tables = append(tables, t)
			}
			sort.Strings(tables)
			for _, t := range tables {
				_, _ = fmt.Fprintf(out, "  %-20s %d row(s)\n", t, rep.Rows[t]) //nolint:errcheck // CLI output
			}
			return nil
		},
	}
	c.Flags().BoolVar(&confirm, "yes", false, "confirm erasure")
	return c
}

// senderKeyLister is implemented by stores that can map a bare sender
// identifier to the transport-namespaced keys holding it.
type senderKeyLister interface {
	SenderKeys(ctx context.Context, sender string) ([]string, error)
}

// resolveSenderKey turns the operator's argument into a stored key. A
// namespaced key is used as given. A bare identifier resolves to the
// one key holding it, and is refused when several transports hold it,
// so one transport's erasure never reaches another's data by accident.
func resolveSenderKey(cmd *cobra.Command, store any, arg string) (string, error) {
	if _, _, ok := senderkey.Split(arg); ok {
		return arg, nil
	}
	lister, ok := store.(senderKeyLister)
	if !ok {
		return arg, nil
	}
	keys, err := lister.SenderKeys(cmd.Context(), arg)
	if err != nil {
		return "", err
	}
	switch len(keys) {
	case 0:
		return arg, nil
	case 1:
		if keys[0] != arg {
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "resolved %s to %s\n", arg, keys[0]) //nolint:errcheck // CLI output
		}
		return keys[0], nil
	default:
		return "", fmt.Errorf("%s is held on several transports (%s); pass one key at a time",
			arg, strings.Join(keys, ", "))
	}
}
