package cli

// The `rousseau a2a` subcommand family — publisher / operator tooling
// for the A2A protocol surfaces (see internal/a2a/*). Mirrors the
// design of `rousseau skills sign`: the daemon itself never signs,
// verify-only is compile-time-guaranteed by the a2a package; this
// CLI is the small standalone tool operators use to prepare cards +
// keys for deployment.
//
// Subcommands:
//
//   rousseau a2a keygen                 → new Ed25519 keypair for card signing
//   rousseau a2a sign-card              → sign an existing AgentCard JSON
//   rousseau a2a verify-card            → verify a served card against a trust list
//   rousseau a2a fetch-card             → fetch + pretty-print a peer's card

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/sebastienrousseau/rousseau-agent/internal/a2a"
)

func newA2ACmd(_ *Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "a2a",
		Short: "Publisher + operator tools for the Agent-to-Agent (A2A) protocol",
		Long: "The A2A protocol (Linux Foundation, see a2a-protocol.org) lets\n" +
			"agents exchange tasks over a well-defined HTTP surface. This\n" +
			"command family manages the operator-side pieces the running\n" +
			"daemon does not do itself — key generation, card signing,\n" +
			"peer-card verification.\n\n" +
			"See docs/a2a.md and docs/a2a-conformance.md for the wire\n" +
			"protocol and rousseau's implementation status.",
	}
	cmd.AddCommand(newA2AKeygenCmd())
	cmd.AddCommand(newA2ASignCardCmd())
	cmd.AddCommand(newA2AVerifyCardCmd())
	cmd.AddCommand(newA2AFetchCardCmd())
	return cmd
}

// -- keygen ---------------------------------------------------------

func newA2AKeygenCmd() *cobra.Command {
	var (
		privOut string
		pubOut  string
	)
	cmd := &cobra.Command{
		Use:   "keygen",
		Short: "Generate an Ed25519 keypair for A2A AgentCard signing",
		Long: "Emits a fresh Ed25519 private + public key encoded as\n" +
			"base64. Feed the private key to `rousseau a2a sign-card` and\n" +
			"distribute the public key via the operator's trust list on\n" +
			"peers that will verify your card.\n\n" +
			"Without --priv-out / --pub-out the keys go to stdout so the\n" +
			"tool composes with pipelines like `... | tee | ...`.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a2aKeygenRun(cmd.OutOrStdout(), cmd.ErrOrStderr(), privOut, pubOut)
		},
	}
	cmd.Flags().StringVar(&privOut, "priv-out", "", "write the private key to this file (default: stdout)")
	cmd.Flags().StringVar(&pubOut, "pub-out", "", "write the public key to this file (default: stdout)")
	return cmd
}

// a2aKeygenRun is the testable body of `a2a keygen`. Split out from
// RunE so tests drive it directly with a caller-supplied writer.
func a2aKeygenRun(stdout, stderr io.Writer, privOut, pubOut string) error {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return fmt.Errorf("a2a keygen: %w", err)
	}
	privB64 := base64.StdEncoding.EncodeToString(priv) + "\n"
	pubB64 := base64.StdEncoding.EncodeToString(pub) + "\n"

	if err := writeKeyOutput(stdout, stderr, "private", privB64, privOut); err != nil {
		return err
	}
	if err := writeKeyOutput(stdout, stderr, "public", pubB64, pubOut); err != nil {
		return err
	}
	return nil
}

// writeKeyOutput is the shared "write to file or stdout, print
// summary to stderr" pattern used by both keys.
func writeKeyOutput(stdout, stderr io.Writer, label, body, dest string) error {
	if dest == "" || dest == "-" {
		if _, err := io.WriteString(stdout, body); err != nil {
			return err
		}
		return nil
	}
	// Private-key files land at 0o600, public keys at 0o644.
	mode := os.FileMode(0o600)
	if label == "public" {
		mode = 0o644
	}
	if err := os.WriteFile(dest, []byte(body), mode); err != nil { //nolint:gosec // publisher-supplied path
		return fmt.Errorf("a2a keygen: write %s: %w", dest, err)
	}
	fmt.Fprintf(stderr, "wrote %s key → %s (mode %o)\n", label, dest, mode) //nolint:errcheck // CLI status message
	return nil
}

// -- sign-card ------------------------------------------------------

func newA2ASignCardCmd() *cobra.Command {
	var (
		cardIn  string
		keyIn   string
		cardOut string
	)
	cmd := &cobra.Command{
		Use:   "sign-card",
		Short: "Sign an A2A AgentCard JSON file with an Ed25519 private key",
		Long: "Reads an AgentCard JSON blob, computes a JWS Compact-form\n" +
			"signature (alg=EdDSA) over the canonical form, and appends\n" +
			"the signature to `signatures[]`. The signed card is written\n" +
			"to --out (or stdout).\n\n" +
			"Chain-of-trust use case: publisher signs, host operators\n" +
			"place the corresponding public key in their trust list to\n" +
			"verify authenticity at fetch time.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if cardIn == "" || keyIn == "" {
				return errors.New("--card and --key are required")
			}
			return a2aSignCardRun(cmd.OutOrStdout(), cardIn, keyIn, cardOut)
		},
	}
	cmd.Flags().StringVar(&cardIn, "card", "", "AgentCard JSON to sign (required)")
	cmd.Flags().StringVar(&keyIn, "key", "", "Ed25519 private key file (base64-encoded, as `rousseau a2a keygen` emits) (required)")
	cmd.Flags().StringVar(&cardOut, "out", "-", "output path for the signed card ('-' for stdout)")
	return cmd
}

func a2aSignCardRun(stdout io.Writer, cardPath, keyPath, outPath string) error {
	cardBytes, err := os.ReadFile(cardPath) //nolint:gosec // publisher-supplied path
	if err != nil {
		return fmt.Errorf("a2a sign-card: read %s: %w", cardPath, err)
	}
	var card a2a.AgentCard
	if err := json.Unmarshal(cardBytes, &card); err != nil {
		return fmt.Errorf("a2a sign-card: parse card: %w", err)
	}
	priv, err := loadA2APrivateKey(keyPath)
	if err != nil {
		return err
	}
	signed, err := a2a.SignAgentCard(card, priv)
	if err != nil {
		return fmt.Errorf("a2a sign-card: %w", err)
	}
	blob, err := json.MarshalIndent(signed, "", "  ")
	if err != nil {
		return fmt.Errorf("a2a sign-card: marshal: %w", err)
	}
	if outPath == "" || outPath == "-" {
		_, err := stdout.Write(append(blob, '\n'))
		return err
	}
	if err := os.WriteFile(outPath, append(blob, '\n'), 0o644); err != nil { //nolint:gosec // publisher-supplied path
		return fmt.Errorf("a2a sign-card: write %s: %w", outPath, err)
	}
	return nil
}

// -- verify-card ----------------------------------------------------

func newA2AVerifyCardCmd() *cobra.Command {
	var (
		cardIn string
		trust  []string
	)
	cmd := &cobra.Command{
		Use:   "verify-card",
		Short: "Verify a signed A2A AgentCard against a trust list",
		Long: "Reads a card JSON and verifies its signatures[] against the\n" +
			"trusted public keys supplied via --trusted-key (repeatable).\n" +
			"Exits 0 when at least one signature verifies against a\n" +
			"trusted key; exits non-zero on any failure. The specific\n" +
			"failure mode (unsigned, untrusted key, tampered payload)\n" +
			"is printed to stderr so CI pipelines can filter.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if cardIn == "" {
				return errors.New("--card is required")
			}
			if len(trust) == 0 {
				return errors.New("at least one --trusted-key is required")
			}
			return a2aVerifyCardRun(cmd.OutOrStdout(), cmd.ErrOrStderr(), cardIn, trust)
		},
	}
	cmd.Flags().StringVar(&cardIn, "card", "", "AgentCard JSON to verify (required)")
	cmd.Flags().StringSliceVar(&trust, "trusted-key", nil, "base64-encoded Ed25519 public key file (repeatable, at least one required)")
	return cmd
}

func a2aVerifyCardRun(stdout, stderr io.Writer, cardPath string, trustPaths []string) error {
	cardBytes, err := os.ReadFile(cardPath) //nolint:gosec // operator-supplied path
	if err != nil {
		return fmt.Errorf("a2a verify-card: read %s: %w", cardPath, err)
	}
	var card a2a.AgentCard
	if err := json.Unmarshal(cardBytes, &card); err != nil {
		return fmt.Errorf("a2a verify-card: parse card: %w", err)
	}
	trusted, err := loadA2APublicKeys(trustPaths)
	if err != nil {
		return err
	}
	if err := a2a.VerifyAgentCard(card, trusted); err != nil {
		fmt.Fprintln(stderr, "verify-card FAILED:", err) //nolint:errcheck // CLI diagnostic
		return err
	}
	fmt.Fprintf(stdout, "verify-card OK: card %q (version %s) signed by trusted key\n", card.Name, card.Version) //nolint:errcheck // CLI status
	return nil
}

// -- fetch-card -----------------------------------------------------

func newA2AFetchCardCmd() *cobra.Command {
	var (
		endpoint string
		verify   []string
	)
	cmd := &cobra.Command{
		Use:   "fetch-card <peer-url>",
		Short: "Fetch a peer's AgentCard from /.well-known/agent-card.json",
		Long: "Hits the peer's well-known URL and prints the returned\n" +
			"AgentCard as pretty-printed JSON. Optional --trusted-key\n" +
			"(repeatable) enables signature verification against a\n" +
			"trust list — same trust semantics as `verify-card` but\n" +
			"applied to a live peer.\n\n" +
			"Use before configuring a client peer entry so you know\n" +
			"exactly what surface the peer advertises.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			endpoint = args[0]
			return a2aFetchCardRun(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), endpoint, verify)
		},
	}
	cmd.Flags().StringSliceVar(&verify, "trusted-key", nil, "when set, verify the fetched card against these base64-encoded Ed25519 public key files (repeatable)")
	return cmd
}

func a2aFetchCardRun(ctx context.Context, stdout, stderr io.Writer, endpoint string, trustPaths []string) error {
	url := strings.TrimRight(endpoint, "/") + "/.well-known/agent-card.json"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", a2a.ContentTypeSpec+", application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("a2a fetch-card: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10)) //nolint:errcheck // diagnostic only
		return fmt.Errorf("a2a fetch-card: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var card a2a.AgentCard
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&card); err != nil {
		return fmt.Errorf("a2a fetch-card: decode: %w", err)
	}

	if len(trustPaths) > 0 {
		trusted, err := loadA2APublicKeys(trustPaths)
		if err != nil {
			return err
		}
		if err := a2a.VerifyAgentCard(card, trusted); err != nil {
			fmt.Fprintln(stderr, "SIGNATURE VERIFY FAILED:", err) //nolint:errcheck // CLI diagnostic
			return err
		}
		fmt.Fprintln(stderr, "signature verified against trust list") //nolint:errcheck // CLI status
	}

	blob, err := json.MarshalIndent(card, "", "  ")
	if err != nil {
		return err
	}
	_, err = stdout.Write(append(blob, '\n'))
	return err
}

// -- key loaders ----------------------------------------------------

// loadA2APrivateKey reads a base64-std-encoded Ed25519 private key
// file. Mirrors loadEd25519Private from skills.go so publisher CI
// pipelines can reuse the same key file format across `rousseau
// skills sign` and `rousseau a2a sign-card`.
func loadA2APrivateKey(path string) (ed25519.PrivateKey, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // operator-supplied path
	if err != nil {
		return nil, fmt.Errorf("a2a: read key %s: %w", path, err)
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil {
		return nil, fmt.Errorf("a2a: decode key: %w", err)
	}
	if len(decoded) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("a2a: key %s is %d bytes, want %d", path, len(decoded), ed25519.PrivateKeySize)
	}
	return ed25519.PrivateKey(decoded), nil
}

// loadA2APublicKeys reads base64-std-encoded Ed25519 public key files
// (one key per path). Enables trust lists like:
//
//	--trusted-key vendor-a.pub --trusted-key vendor-b.pub
func loadA2APublicKeys(paths []string) ([]ed25519.PublicKey, error) {
	out := make([]ed25519.PublicKey, 0, len(paths))
	for _, p := range paths {
		raw, err := os.ReadFile(p) //nolint:gosec // operator-supplied path
		if err != nil {
			return nil, fmt.Errorf("a2a: read trusted-key %s: %w", p, err)
		}
		decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
		if err != nil {
			return nil, fmt.Errorf("a2a: decode trusted-key %s: %w", p, err)
		}
		if len(decoded) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("a2a: trusted-key %s is %d bytes, want %d", p, len(decoded), ed25519.PublicKeySize)
		}
		out = append(out, ed25519.PublicKey(decoded))
	}
	return out, nil
}
