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
			all, err := loadSkillsFromResolutionChain(opts)
			if err != nil {
				return err
			}
			w := cmd.OutOrStdout()
			if len(all) == 0 {
				fmt.Fprintln(w, "(no skills)") //nolint:errcheck // CLI output
				return nil
			}
			for _, s := range all {
				fmt.Fprintf(w, "%-20s  triggers=%s\n    %s\n", //nolint:errcheck // CLI output
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
			all, err := loadSkillsFromResolutionChain(opts)
			if err != nil {
				return err
			}
			for _, s := range all {
				if s.Name == args[0] {
					fmt.Fprintln(cmd.OutOrStdout(), s.Body) //nolint:errcheck // CLI output
					return nil
				}
			}
			return fmt.Errorf("skill %q not found", args[0])
		},
	}
}

// resolveSkillsDir returns the primary skills-dir chosen from
// config or the default user location. It is retained for callers
// that want the "first" location; loadSkillsFromResolutionChain
// prefers this dir AND overlays the system-wide fallback bundle
// at /etc/rousseau/skills/.
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

// systemSkillsDir is the fallback location the container image
// populates with the bundled starter skills (see skills/README.md).
//
// It is a var rather than a const so tests can point it at an empty
// directory. Without that, any test asserting on skill-list output is
// not hermetic: it passes on a bare workstation and fails inside the
// container image, which does populate /etc/rousseau/skills. Use
// withSystemSkillsDir in tests rather than assigning to this directly.
var systemSkillsDir = "/etc/rousseau/skills"

// loadSkillsFromResolutionChain returns skills from the primary
// user location first, then overlays the system bundle. User skills
// shadow system skills with the same Name — Load deduplicates by
// path but not by name, so we do the name-dedupe here.
func loadSkillsFromResolutionChain(opts *Options) ([]skills.Skill, error) {
	primary, err := skills.Load(resolveSkillsDir(opts))
	if err != nil {
		return nil, err
	}
	system, sysErr := skills.Load(systemSkillsDir)
	if sysErr != nil {
		// System skills dir missing / unreadable is not fatal — the
		// user's own skills are enough.
		if opts != nil && opts.Logger != nil {
			opts.Logger.Debug("skills.system_load_skipped", "err", sysErr.Error())
		}
		return primary, nil
	}
	if len(system) == 0 {
		return primary, nil
	}
	seen := make(map[string]struct{}, len(primary))
	for _, s := range primary {
		seen[s.Name] = struct{}{}
	}
	out := append([]skills.Skill(nil), primary...)
	for _, s := range system {
		if _, dup := seen[s.Name]; dup {
			continue // user override wins
		}
		out = append(out, s)
	}
	return out, nil
}
