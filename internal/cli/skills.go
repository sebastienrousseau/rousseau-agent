package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/sebastienrousseau/rousseau-agent/internal/skills"
)

func newSkillsCmd(opts *Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "skills",
		Short: "Inspect user-authored skills",
		Long: "Skills are Markdown files under agent.skills_dir (default:\n" +
			"$XDG_DATA_HOME/rousseau/skills). Files with front-matter triggers\n" +
			"activate automatically when a user message matches. See\n" +
			"docs/COMPETITORS.md §1.5 for the format.",
	}
	cmd.AddCommand(newSkillsListCmd(opts))
	cmd.AddCommand(newSkillsShowCmd(opts))
	return cmd
}

func newSkillsListCmd(opts *Options) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List available skills",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			all, err := skills.Load(resolveSkillsDir(opts))
			if err != nil {
				return err
			}
			w := cmd.OutOrStdout()
			if len(all) == 0 {
				fmt.Fprintln(w, "(no skills)")
				return nil
			}
			for _, s := range all {
				fmt.Fprintf(w, "%-20s  triggers=%s\n    %s\n",
					s.Name, strings.Join(s.Triggers, ","), s.Description)
			}
			return nil
		},
	}
}

func newSkillsShowCmd(opts *Options) *cobra.Command {
	return &cobra.Command{
		Use:   "show <name>",
		Short: "Print the full body of a skill",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			all, err := skills.Load(resolveSkillsDir(opts))
			if err != nil {
				return err
			}
			for _, s := range all {
				if s.Name == args[0] {
					fmt.Fprintln(cmd.OutOrStdout(), s.Body)
					return nil
				}
			}
			return fmt.Errorf("skill %q not found", args[0])
		},
	}
}

func resolveSkillsDir(opts *Options) string {
	if opts.Config != nil && opts.Config.Agent.SkillsDir != "" {
		return opts.Config.Agent.SkillsDir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".local", "share", "rousseau", "skills")
}
