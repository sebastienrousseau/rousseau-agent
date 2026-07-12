package cli

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/google/uuid"
	"github.com/robfig/cron/v3"
	"github.com/spf13/cobra"

	sqlitestore "github.com/sebastienrousseau/rousseau-agent/internal/state/sqlite"
)

func newCronCmd(opts *Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cron",
		Short: "Manage scheduled prompts",
		Long: "cron stores prompt-on-a-schedule jobs. A running `rousseau whatsapp`\n" +
			"daemon picks them up and fires them via the configured provider,\n" +
			"delivering the result to the configured target (a WhatsApp JID today).",
	}
	cmd.AddCommand(newCronAddCmd(opts))
	cmd.AddCommand(newCronListCmd(opts))
	cmd.AddCommand(newCronRemoveCmd(opts))
	cmd.AddCommand(newCronToggleCmd(opts, true))
	cmd.AddCommand(newCronToggleCmd(opts, false))
	return cmd
}

func newCronAddCmd(opts *Options) *cobra.Command {
	var (
		name      string
		schedule  string
		prompt    string
		deliverTo string
	)
	c := &cobra.Command{
		Use:   "add",
		Short: "Add a scheduled prompt",
		Long:  "Validates the cron expression against robfig/cron/v3's parser before persisting.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if name == "" || schedule == "" || prompt == "" {
				return errors.New("--name, --schedule and --prompt are required")
			}
			if _, err := cron.ParseStandard(schedule); err != nil {
				return fmt.Errorf("invalid cron expression: %w", err)
			}
			store, err := openCronStore(cmd.Context(), opts)
			if err != nil {
				return err
			}
			defer func() { _ = store.Close() }() //nolint:errcheck // best-effort cleanup

			cs, err := sqlitestore.NewCronStore(cmd.Context(), store)
			if err != nil {
				return err
			}
			job := sqlitestore.CronJob{
				ID:        uuid.NewString(),
				Name:      name,
				CronExpr:  schedule,
				Prompt:    prompt,
				DeliverTo: deliverTo,
				Enabled:   true,
			}
			if err := cs.Put(cmd.Context(), job); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "added %s (%s) → %s\n", job.Name, job.CronExpr, job.ID) //nolint:errcheck // CLI output; stdout write failures are unrecoverable
			return nil
		},
	}
	c.Flags().StringVar(&name, "name", "", "unique name")
	c.Flags().StringVar(&schedule, "schedule", "", "5-field cron expression (min hour dom mon dow)")
	c.Flags().StringVar(&prompt, "prompt", "", "prompt to run")
	c.Flags().StringVar(&deliverTo, "deliver-to", "", "WhatsApp JID to deliver the result to")
	return c
}

func newCronListCmd(opts *Options) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List every scheduled job",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			store, err := openCronStore(cmd.Context(), opts)
			if err != nil {
				return err
			}
			defer func() { _ = store.Close() }() //nolint:errcheck // best-effort cleanup

			cs, err := sqlitestore.NewCronStore(cmd.Context(), store)
			if err != nil {
				return err
			}
			jobs, err := cs.List(cmd.Context())
			if err != nil {
				return err
			}
			w := cmd.OutOrStdout()
			if len(jobs) == 0 {
				fmt.Fprintln(w, "(no jobs)") //nolint:errcheck // CLI output; stdout write failures are unrecoverable
				return nil
			}
			for _, j := range jobs {
				status := "on"
				if !j.Enabled {
					status = "off"
				}
				last := "never"
				if j.LastRunAt != nil {
					last = j.LastRunAt.Format("2006-01-02 15:04")
				}
				fmt.Fprintf(w, "%s  %-3s  %-20s  %-20s  last=%s\n    %s → %s\n", //nolint:errcheck // CLI output; stdout write failures are unrecoverable
					shortID(j.ID), status, j.Name, j.CronExpr, last, j.Prompt, j.DeliverTo)
			}
			return nil
		},
	}
}

func newCronRemoveCmd(opts *Options) *cobra.Command {
	return &cobra.Command{
		Use:   "remove <name-or-id>",
		Short: "Delete a scheduled job",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := openCronStore(cmd.Context(), opts)
			if err != nil {
				return err
			}
			defer func() { _ = store.Close() }() //nolint:errcheck // best-effort cleanup
			cs, err := sqlitestore.NewCronStore(cmd.Context(), store)
			if err != nil {
				return err
			}
			return cs.Delete(cmd.Context(), args[0])
		},
	}
}

func newCronToggleCmd(opts *Options, enable bool) *cobra.Command {
	name := "disable"
	short := "Disable a scheduled job"
	if enable {
		name = "enable"
		short = "Enable a scheduled job"
	}
	return &cobra.Command{
		Use:   name + " <name-or-id>",
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := openCronStore(cmd.Context(), opts)
			if err != nil {
				return err
			}
			defer func() { _ = store.Close() }() //nolint:errcheck // best-effort cleanup
			cs, err := sqlitestore.NewCronStore(cmd.Context(), store)
			if err != nil {
				return err
			}
			return cs.SetEnabled(cmd.Context(), args[0], enable)
		},
	}
}

func openCronStore(ctx context.Context, opts *Options) (*sqlitestore.Store, error) {
	path := opts.Config.State.Path
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		path = home + "/.local/share/rousseau/sessions.db"
	}
	return sqlitestore.Open(ctx, path)
}
