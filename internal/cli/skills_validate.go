package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/spf13/cobra"

	"github.com/sebastienrousseau/rousseau-agent/internal/skills"
)

// newSkillsValidateCmd wires the `rousseau skills validate` command:
// walk the operator's skills directory using the spec-compliant
// discovery layer, report per-skill parse / validation results,
// and exit non-zero when at least one skill is invalid.
//
// Two output modes:
//
//   - Human (default) — one line per skill with a ✔/✘ marker and a
//     summary count. Read by an operator on a workstation.
//   - JSON (--json) — a stable JSON array of {path, name, ok, err}
//     records, ordered by path. Used by CI, editors, or a git
//     pre-commit hook that wants to gate on skill validity without
//     parsing text.
//
// The command deliberately runs the spec-compliant discovery
// walker even when agent.skills_mode is "legacy" — the point of
// `validate` is to help operators migrate to spec mode by
// surfacing exactly which of their existing skill files would
// fail the stricter checks. Running it against a legacy-shaped
// tree therefore shows every flat *.md as "skipped, not a skill
// directory" plus errors for whatever malformed frontmatter is
// present.
func newSkillsValidateCmd(opts *Options) *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "validate [dir]",
		Short: "Validate SKILL.md files against the agentskills.io spec",
		Long: `Walk the given directory (or agent.skills_dir when no argument is
given) and report which skills parse + validate against the
agentskills.io three-tier spec. Exits non-zero when at least one
skill fails, so CI / pre-commit hooks can gate on skill validity.

Human output:

  Validating /home/seb/.rousseau/skills ...

    ✔ greet                Greet the user warmly.
    ✘ typoed               SKILL.md frontmatter has unknown field "descripton"
    ✘ mismatched           name "wrong" does not match containing directory "mismatched"

  Summary: 1 valid, 2 invalid (3 total)

JSON output (--json):

  [
    {"path":"/skills/greet/SKILL.md","name":"greet","ok":true,"description":"Greet the user warmly."},
    {"path":"/skills/typoed/SKILL.md","ok":false,"err":"..."}
  ]

Both modes exit 1 on any invalid skill.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := resolveValidateDir(opts, args)
			return runSkillsValidate(cmd.OutOrStdout(), cmd.ErrOrStderr(), dir, jsonOut)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit machine-readable JSON instead of human text")
	return cmd
}

// resolveValidateDir picks the directory to walk. Explicit CLI
// argument wins; otherwise fall back to the operator's configured
// SkillsDir (or the default user location via resolveSkillsDir).
func resolveValidateDir(opts *Options, args []string) string {
	if len(args) == 1 && args[0] != "" {
		return args[0]
	}
	return resolveSkillsDir(opts)
}

// validationRecord is the JSON shape emitted by --json mode. Kept
// as a package-private type because the CLI is the only consumer;
// external tools should treat it as informational, not a stable
// contract.
type validationRecord struct {
	Path        string `json:"path"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	OK          bool   `json:"ok"`
	Err         string `json:"err,omitempty"`
}

// runSkillsValidate is the RunE body extracted so unit tests can
// drive it against a tempdir without invoking cobra. Returns the
// same non-zero-exit error shape cobra surfaces via os.Exit(1)
// when at least one skill failed.
func runSkillsValidate(stdout, stderr io.Writer, dir string, jsonOut bool) error {
	if dir == "" {
		return fmt.Errorf("no skills directory to validate — pass a path or set agent.skills_dir")
	}
	if _, err := os.Stat(dir); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("skills directory %s does not exist", dir)
		}
		return fmt.Errorf("stat skills directory %s: %w", dir, err)
	}

	// Collect both the valid skills (from DiscoverSpec's normal
	// return) and the invalid ones (via OnInvalid). Discovery is
	// stable-sorted by path, so both slices land in deterministic
	// order.
	var records []validationRecord

	valid, discoverErr := skills.DiscoverSpec(dir, skills.DiscoverOptions{
		OnInvalid: func(path string, err error) {
			records = append(records, validationRecord{
				Path: path,
				OK:   false,
				Err:  err.Error(),
			})
		},
	})
	if discoverErr != nil {
		return fmt.Errorf("discover skills under %s: %w", dir, discoverErr)
	}
	for _, s := range valid {
		records = append(records, validationRecord{
			Path:        filepath.Join(s.BaseDir, "SKILL.md"),
			Name:        s.Name,
			Description: s.Description,
			OK:          true,
		})
	}

	// Deterministic output: sort by Path so re-runs produce the
	// same order regardless of walker order. Callers can then
	// diff two invocations meaningfully.
	sort.Slice(records, func(i, j int) bool { return records[i].Path < records[j].Path })

	invalid := 0
	for _, r := range records {
		if !r.OK {
			invalid++
		}
	}

	if jsonOut {
		return emitJSON(stdout, records, invalid)
	}
	return emitHuman(stdout, stderr, dir, records, invalid)
}

// emitJSON writes the records as an indented JSON array. Returns
// a non-nil error when any record failed so cobra exits non-zero.
func emitJSON(w io.Writer, records []validationRecord, invalid int) error {
	// Emit an empty array for "no skills found" so a `jq length`
	// or similar produces 0, not "null".
	if records == nil {
		records = []validationRecord{}
	}
	body, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal validation records: %w", err)
	}
	if _, err := w.Write(append(body, '\n')); err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	if invalid > 0 {
		return fmt.Errorf("%d skill(s) failed validation", invalid)
	}
	return nil
}

// emitHuman renders the human-readable form: header, one line per
// skill with ✔/✘ marker, summary count. Uses stderr for the header
// and stdout for the per-skill body so `| jq` / `| grep` in JSON
// mode's cousin remain scriptable — separates operator commentary
// from data.
func emitHuman(stdout, stderr io.Writer, dir string, records []validationRecord, invalid int) error {
	fmt.Fprintf(stderr, "Validating skills under %s ...\n\n", dir) //nolint:errcheck // CLI progress
	if len(records) == 0 {
		fmt.Fprintln(stderr, "(no skills found)") //nolint:errcheck
		return nil
	}
	for _, r := range records {
		if r.OK {
			// Truncate description at 70 chars so long lines don't
			// wrap ugly on 80-col terminals.
			desc := truncateForList(r.Description, 70)
			fmt.Fprintf(stdout, "  ✔ %-30s %s\n", r.Name, desc) //nolint:errcheck
		} else {
			label := labelForInvalid(r)
			fmt.Fprintf(stdout, "  ✘ %-30s %s\n", label, r.Err) //nolint:errcheck
		}
	}
	fmt.Fprintf(stderr, "\nSummary: %d valid, %d invalid (%d total)\n", //nolint:errcheck
		len(records)-invalid, invalid, len(records))
	if invalid > 0 {
		return fmt.Errorf("%d skill(s) failed validation", invalid)
	}
	return nil
}

// labelForInvalid picks a human-readable identifier for a failed
// skill. Prefers Name when the parser got that far; falls back to
// the parent directory name from the SKILL.md path. Prevents an
// entire column of "(unknown)" entries when several skills fail.
func labelForInvalid(r validationRecord) string {
	if r.Name != "" {
		return r.Name
	}
	return filepath.Base(filepath.Dir(r.Path))
}

// truncateForList shortens a string to at most n runes plus a "…"
// suffix. Uses runes so a multi-byte utf-8 description cuts cleanly
// on a boundary rather than half a codepoint.
func truncateForList(s string, n int) string {
	if n <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "…"
}
