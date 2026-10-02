package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	pgstore "github.com/sebastienrousseau/rousseau-agent/internal/state/postgres"
	sqlitestore "github.com/sebastienrousseau/rousseau-agent/internal/state/sqlite"
)

// newMigrateCmd upgrades the session store to the current schema
// (append-only messages, transport-namespaced sender keys) or, with
// --down, returns it to the previous one.
func newMigrateCmd(opts *Options) *cobra.Command {
	var (
		dryRun, down, backupTaken bool
		mapping                   []string
	)
	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "Upgrade the session store to the current schema",
		Long: "Moves each conversation's messages into their own rows and prefixes every\n" +
			"sender key with its transport (signal:+44...). Stop the daemon first.\n\n" +
			"SQLite: the store is backed up next to itself (sessions.db.pre-v2-<time>) and\n" +
			"checked before anything changes. Postgres: take a pg_dump yourself and pass\n" +
			"--backup-taken. Either way the upgrade runs in one transaction and verifies\n" +
			"every session before it commits; any failure leaves the store as it was.\n\n" +
			"A sender whose transport cannot be told from its form (a phone number may be\n" +
			"Signal or iMessage) needs --map <sender>=<transport>. Run --dry-run first to\n" +
			"see them. --down returns an upgraded store to the previous layout for an\n" +
			"older binary.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			m, err := parseMap(mapping)
			if err != nil {
				return err
			}
			cfg := opts.Config.State
			out := cmd.OutOrStdout()
			switch driverName(cfg) {
			case "sqlite":
				path, err := statePath(cfg.Path)
				if err != nil {
					return err
				}
				if down {
					rep, err := sqlitestore.MigrateDown(cmd.Context(), path, nil)
					if err != nil {
						return err
					}
					fprintf(out, "returned %s to schema version 1: %d session(s), %d message(s)\nbackup: %s\n",
						path, rep.Sessions, rep.Messages, rep.Backup)
					return nil
				}
				if _, err := os.Stat(path); err != nil {
					return fmt.Errorf("no session store at %s: %w", path, err)
				}
				rep, err := sqlitestore.Migrate(cmd.Context(), path, sqlitestore.MigrateOptions{DryRun: dryRun, Map: m})
				printReport(out, path, rep, dryRun, err)
				return migrateErr(err)
			case "postgres":
				if down {
					rep, err := pgstore.MigrateDown(cmd.Context(), cfg.DSN, backupTaken)
					if err != nil {
						return err
					}
					fprintf(out, "returned the Postgres store to schema version 1: %d session(s), %d message(s)\n",
						rep.Sessions, rep.Messages)
					return nil
				}
				rep, err := pgstore.Migrate(cmd.Context(), cfg.DSN, pgstore.MigrateOptions{DryRun: dryRun, Map: m, BackupTaken: backupTaken})
				printReport(out, "the Postgres store", rep, dryRun, err)
				return migrateErr(err)
			default:
				return fmt.Errorf("unknown state driver %q", driverName(cfg))
			}
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "report what would change; write nothing")
	cmd.Flags().StringArrayVar(&mapping, "map", nil, "assign a transport to an ambiguous sender: <sender>=<transport> (repeatable)")
	cmd.Flags().BoolVar(&down, "down", false, "return an upgraded store to the previous schema")
	cmd.Flags().BoolVar(&backupTaken, "backup-taken", false, "Postgres: confirm a pg_dump of the database exists")
	return cmd
}

func migrateErr(err error) error {
	if errors.Is(err, sqlitestore.ErrAmbiguousSenders) {
		return errors.New("nothing changed: assign each ambiguous sender a transport with --map <sender>=<transport>")
	}
	return err
}

func parseMap(pairs []string) (map[string]string, error) {
	m := map[string]string{}
	for _, p := range pairs {
		i := strings.LastIndex(p, "=")
		if i <= 0 || i == len(p)-1 {
			return nil, fmt.Errorf("--map %q: want <sender>=<transport>", p)
		}
		m[p[:i]] = p[i+1:]
	}
	return m, nil
}

func statePath(path string) (string, error) {
	if path != "" {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home: %w", err)
	}
	return filepath.Join(home, ".local", "share", "rousseau", "sessions.db"), nil
}

func fprintf(w io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(w, format, args...) //nolint:errcheck // CLI output
}

func printReport(w io.Writer, where string, rep sqlitestore.MigrateReport, dryRun bool, err error) {
	if err == nil && rep.FromVersion >= rep.ToVersion && rep.FromVersion > 0 && len(rep.Keys) == 0 && rep.Sessions == 0 {
		fprintf(w, "%s is already at schema version %d; nothing to do\n", where, rep.FromVersion)
		return
	}
	verb := "migrated"
	switch {
	case dryRun:
		verb = "dry run, nothing written:"
	case err != nil:
		verb = "NOT migrated (nothing changed):"
	}
	fprintf(w, "%s %s from schema version %d to %d\n", verb, where, rep.FromVersion, rep.ToVersion)
	fprintf(w, "  sessions: %d, messages: %d\n", rep.Sessions, rep.Messages)
	for _, k := range rep.Keys {
		fprintf(w, "  key  %s -> %s (%s)\n", k.From, k.To, k.Rule)
	}
	for _, a := range rep.Ambiguous {
		fprintf(w, "  AMBIGUOUS  %s could be %s: add --map %s=<%s>\n",
			a.Sender, strings.Join(a.Candidates, " or "), a.Sender, strings.Join(a.Candidates, "|"))
	}
	if rep.Backup != "" {
		fprintf(w, "  backup: %s\n", rep.Backup)
	}
}
