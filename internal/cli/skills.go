package cli

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/sebastienrousseau/rousseau-agent/internal/skills"
	"github.com/sebastienrousseau/rousseau-agent/internal/skills/bundle"
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
	cmd.AddCommand(newSkillsSignCmd())
	return cmd
}

// newSkillsSignCmd wraps the [bundle.Sign] primitive so CI
// publisher pipelines can produce signed .skill.json bundles
// without importing the Go package. Reads a JSON manifest
// (name / version / publisher / triggers / etc.) + a content
// path + a private-key path, populates the content + SBOM
// hashes, signs, and writes the assembled bundle to stdout
// or --out.
//
// Design choices worth noting:
//
//   - Reads the private key as a raw 64-byte Ed25519 key
//     (base64 std) — matches what `openssl genpkey -algorithm
//     Ed25519 | openssl pkey -text` produces after trimming.
//     Not PEM. The bundle format uses the same encoding so
//     publisher tooling stays consistent.
//   - The manifest input is the SAME shape as bundle.Manifest
//     minus the two `_sha256` fields — those get filled by
//     PopulateHashes. Publisher writes what they know; the
//     tool computes what it needs.
//   - Content is passed as a file path (not stdin) because
//     signing a skill directly from a Markdown file the
//     publisher has curated is the common shape. --content-file
//     is required; empty content isn't meaningful.
//   - SBOM is optional; when --sbom-file is set the loader
//     reads it as JSON and both hashes are pinned.
func newSkillsSignCmd() *cobra.Command {
	var (
		manifestFile string
		contentFile  string
		sbomFile     string
		keyFile      string
		outFile      string
	)
	cmd := &cobra.Command{
		Use:   "sign",
		Short: "Sign a skill bundle for enterprise distribution",
		Long: "Produce a .skill.json bundle from a manifest + content (+ optional SBOM)\n" +
			"signed with an Ed25519 private key.\n\n" +
			"The manifest JSON follows the bundle.Manifest shape minus the\n" +
			"content_sha256 and sbom_sha256 fields — those are filled by this\n" +
			"tool. Example manifest:\n\n" +
			"  {\n" +
			"    \"name\":         \"git-rebase\",\n" +
			"    \"version\":      \"1.2.0\",\n" +
			"    \"publisher\":    \"vendor-example\",\n" +
			"    \"published_at\": \"2026-08-15T00:00:00Z\",\n" +
			"    \"description\": \"Guide git rebase safely.\",\n" +
			"    \"triggers\":    [\"rebase\", \"git rebase\"]\n" +
			"  }\n\n" +
			"The private key is a base64-encoded raw Ed25519 secret key (64\n" +
			"bytes decoded). Generate one with:\n\n" +
			"  openssl genpkey -algorithm Ed25519 -out sk.pem\n" +
			"  # extract the raw 64-byte secret (see the bundle README for a\n" +
			"  # portable one-liner)\n\n" +
			"Distribute the corresponding public key via the operator's\n" +
			"agent.skill_bundles.trusted_publisher_keys allowlist.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if manifestFile == "" || contentFile == "" || keyFile == "" {
				return errors.New("--manifest, --content-file, and --key are all required")
			}
			b, err := assembleBundle(manifestFile, contentFile, sbomFile, keyFile)
			if err != nil {
				return err
			}
			out, err := json.MarshalIndent(b, "", "  ")
			if err != nil {
				return fmt.Errorf("marshal bundle: %w", err)
			}
			if outFile == "" || outFile == "-" {
				_, err := cmd.OutOrStdout().Write(append(out, '\n'))
				return err
			}
			if err := os.WriteFile(outFile, append(out, '\n'), 0o644); err != nil { //nolint:gosec // publisher-supplied out path
				return fmt.Errorf("write %s: %w", outFile, err)
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "signed bundle → %s\n", outFile) //nolint:errcheck // CLI progress message
			return nil
		},
	}
	cmd.Flags().StringVar(&manifestFile, "manifest", "", "JSON manifest file (required)")
	cmd.Flags().StringVar(&contentFile, "content-file", "", "skill body / markdown file (required)")
	cmd.Flags().StringVar(&sbomFile, "sbom-file", "", "optional CycloneDX SBOM JSON to bundle")
	cmd.Flags().StringVar(&keyFile, "key", "", "base64-encoded Ed25519 private key file (required)")
	cmd.Flags().StringVar(&outFile, "out", "-", "output path for the signed .skill.json ('-' for stdout)")
	return cmd
}

// assembleBundle wires the manifest + content + SBOM + key
// into a signed [bundle.Bundle]. Split out from RunE so unit
// tests can drive it without invoking cobra.
func assembleBundle(manifestFile, contentFile, sbomFile, keyFile string) (*bundle.Bundle, error) {
	manifestBytes, err := os.ReadFile(manifestFile) //nolint:gosec // publisher-supplied path
	if err != nil {
		return nil, fmt.Errorf("read manifest %s: %w", manifestFile, err)
	}
	var manifest bundle.Manifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}
	content, err := os.ReadFile(contentFile) //nolint:gosec // publisher-supplied path
	if err != nil {
		return nil, fmt.Errorf("read content %s: %w", contentFile, err)
	}
	b := &bundle.Bundle{
		Manifest: manifest,
		Content:  string(content),
	}
	if sbomFile != "" {
		sbom, err := os.ReadFile(sbomFile) //nolint:gosec // publisher-supplied path
		if err != nil {
			return nil, fmt.Errorf("read sbom %s: %w", sbomFile, err)
		}
		// Validate SBOM is legal JSON so we don't ship a
		// broken payload to operators.
		var probe any
		if err := json.Unmarshal(sbom, &probe); err != nil {
			return nil, fmt.Errorf("sbom %s is not valid JSON: %w", sbomFile, err)
		}
		b.SBOM = sbom
	}
	b.PopulateHashes()
	// Validate manifest before signing so a publisher-tooling
	// bug (missing name / version / etc.) fails at sign time
	// rather than at operator-install time.
	if err := b.Validate(); err != nil {
		return nil, err
	}

	priv, err := loadEd25519Private(keyFile)
	if err != nil {
		return nil, err
	}
	b.Signature = bundle.Sign(b.Manifest, priv)

	// Belt-and-braces: verify against our own public key
	// before writing. Catches a class of publisher-tooling
	// bug (wrong file order, corrupted key) at sign time
	// rather than at operator-install time.
	pub := priv.Public().(ed25519.PublicKey)
	if err := b.Verify([]ed25519.PublicKey{pub}); err != nil {
		return nil, fmt.Errorf("self-verify after sign: %w", err)
	}
	return b, nil
}

// loadEd25519Private reads a base64-std-encoded Ed25519
// private key file. Whitespace and trailing newlines are
// stripped so files edited in a text editor Just Work.
func loadEd25519Private(path string) (ed25519.PrivateKey, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // publisher-supplied path
	if err != nil {
		return nil, fmt.Errorf("read key %s: %w", path, err)
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil {
		return nil, fmt.Errorf("decode key: %w", err)
	}
	if len(decoded) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("key %s is %d bytes, want %d (Ed25519 raw private key)",
			path, len(decoded), ed25519.PrivateKeySize)
	}
	return ed25519.PrivateKey(decoded), nil
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
