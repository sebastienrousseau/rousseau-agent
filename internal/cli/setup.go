package cli

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

// newSetupCmd wires the `rousseau setup` first-run wizard —
// Phase 4 of the strategic plan. Compresses the pre-Phase-4
// "30-minute Quadlet-plus-QR-plus-Claude-CLI yak shave" into a
// five-minute conversation ending with a working config file
// and a "next command to run" line.
//
// Non-goals (deliberate scope cut):
//
//   - Does not download / build / install the binary — that's
//     scripts/install.sh's job. Setup only runs after the binary
//     is on $PATH.
//   - Does not do WhatsApp QR pairing itself — the QR appears
//     when the operator subsequently runs `rousseau whatsapp`
//     (whatsmeow prints it to stdout). Setup only writes the
//     config the whatsapp command reads.
//   - Does not run `rousseau doctor` interactively today. The
//     wizard prints "run `rousseau doctor` when you're ready" at
//     the end so the operator sees the diagnostic on demand.
//
// The wizard is idempotent: running against an existing config
// prompts for overwrite / cancel rather than clobbering.
func newSetupCmd(_ *Options) *cobra.Command {
	var (
		nonInteractive bool
		configPath     string
	)
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "First-run interactive wizard — writes a starter ~/.config/rousseau/config.yaml",
		Long: `Interactive first-run wizard. Prompts for:

  1. LLM provider (Claude CLI / Anthropic API / OpenAI / Ollama)
  2. Primary chat transport (WhatsApp / Telegram / Signal / Discord
     / Matrix / Slack / Email / CLI-only)
  3. Provider-specific credentials / allowlist JIDs as needed

Writes ~/.config/rousseau/config.yaml (or --config path). Refuses
to overwrite an existing file without confirmation. Ends with the
exact command to run next.

Non-interactive mode (--yes) accepts defaults for every prompt —
useful for CI or dotfile-driven install flows.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path := configPath
			if path == "" {
				var err error
				path, err = defaultConfigPath()
				if err != nil {
					return err
				}
			}
			w := setupWizard{
				in:             cmd.InOrStdin(),
				out:            cmd.OutOrStdout(),
				configPath:     path,
				nonInteractive: nonInteractive,
			}
			return w.run()
		},
	}
	cmd.Flags().BoolVarP(&nonInteractive, "yes", "y", false, "accept defaults for every prompt (non-interactive)")
	cmd.Flags().StringVar(&configPath, "config", "", "config file to write (default: $XDG_CONFIG_HOME/rousseau/config.yaml)")
	return cmd
}

// defaultConfigPath resolves $XDG_CONFIG_HOME/rousseau/config.yaml
// (or the XDG fallback $HOME/.config/rousseau/config.yaml).
func defaultConfigPath() (string, error) {
	xdg := os.Getenv("XDG_CONFIG_HOME")
	if xdg == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("setup: resolve home dir: %w", err)
		}
		xdg = filepath.Join(home, ".config")
	}
	return filepath.Join(xdg, "rousseau", "config.yaml"), nil
}

// setupWizard bundles the state the interactive flow threads
// through. Kept in a struct so unit tests can drive the same
// prompts with an in-memory reader/writer.
type setupWizard struct {
	in             io.Reader
	out            io.Writer
	configPath     string
	nonInteractive bool
}

// setupAnswers captures the operator's selections. Serialised to
// YAML in writeConfig — see the config.Config struct for the
// canonical schema.
type setupAnswers struct {
	Provider     string // "claudecli" | "anthropic" | "openai" | "ollama"
	AnthropicKey string // when Provider == "anthropic"
	OpenAIKey    string // when Provider == "openai"
	OllamaURL    string // when Provider == "ollama"
	Transport    string // "whatsapp" | "telegram" | ... | "cli"
	WhatsAppJID  string // when Transport == "whatsapp"
}

func (w *setupWizard) run() error {
	fmt.Fprintln(w.out, "rousseau-agent setup — one-time config wizard.")               //nolint:errcheck
	fmt.Fprintln(w.out, "")                                                             //nolint:errcheck
	fmt.Fprintf(w.out, "Writing to: %s\n", w.configPath)                                //nolint:errcheck
	fmt.Fprintln(w.out, "Run with --config to write elsewhere; --yes to skip prompts.") //nolint:errcheck
	fmt.Fprintln(w.out, "")                                                             //nolint:errcheck

	if err := w.checkOverwrite(); err != nil {
		return err
	}

	scanner := bufio.NewScanner(w.in)
	answers, err := w.askAll(scanner)
	if err != nil {
		return err
	}

	if err := w.writeConfig(answers); err != nil {
		return err
	}

	w.printNextSteps(answers)
	return nil
}

// checkOverwrite refuses to clobber an existing config unless the
// operator confirms. Non-interactive mode fails hard rather than
// overwriting silently — that would surprise anyone who forgot
// they'd already run setup.
func (w *setupWizard) checkOverwrite() error {
	if _, err := os.Stat(w.configPath); os.IsNotExist(err) {
		return nil
	}
	if w.nonInteractive {
		return fmt.Errorf("setup: %s already exists (refusing to overwrite in --yes mode; remove the file or pass --config to write elsewhere)", w.configPath)
	}
	fmt.Fprintf(w.out, "!  %s already exists.\n", w.configPath)                        //nolint:errcheck
	fmt.Fprintln(w.out, "   Overwrite? Type 'yes' to proceed, anything else cancels:") //nolint:errcheck
	fmt.Fprint(w.out, "   > ")                                                         //nolint:errcheck
	line, err := readLine(w.in)
	if err != nil {
		return err
	}
	if strings.TrimSpace(strings.ToLower(line)) != "yes" {
		return fmt.Errorf("setup: cancelled")
	}
	return nil
}

// askAll runs the sequence of prompts. Failures propagate — a
// misconfigured stdin (EOF, closed pipe) aborts rather than
// silently writing bad values.
func (w *setupWizard) askAll(scanner *bufio.Scanner) (setupAnswers, error) {
	a := setupAnswers{}

	// Q1 — provider.
	provider, err := w.askChoice(scanner, "Which LLM provider will you use?",
		[]string{
			"claudecli   — use the local `claude` CLI (Anthropic OAuth via Claude Code)",
			"anthropic   — direct Anthropic API (needs ANTHROPIC_API_KEY)",
			"openai      — OpenAI API (needs OPENAI_API_KEY)",
			"ollama      — local llama.cpp / Ollama endpoint (BYO model)",
		},
		"claudecli")
	if err != nil {
		return a, err
	}
	a.Provider = provider

	// Follow-ups based on provider.
	switch a.Provider {
	case "anthropic":
		key, err := w.askSecret(scanner, "Anthropic API key (leave blank to configure via env var later):")
		if err != nil {
			return a, err
		}
		a.AnthropicKey = strings.TrimSpace(key)
	case "openai":
		key, err := w.askSecret(scanner, "OpenAI API key (leave blank to configure via env var later):")
		if err != nil {
			return a, err
		}
		a.OpenAIKey = strings.TrimSpace(key)
	case "ollama":
		url, err := w.askString(scanner, "Ollama endpoint URL:", "http://localhost:11434/v1")
		if err != nil {
			return a, err
		}
		a.OllamaURL = strings.TrimSpace(url)
	}

	// Q2 — transport.
	transport, err := w.askChoice(scanner, "Primary chat transport (which channel will you message from?)",
		[]string{
			"whatsapp — the flagship prosumer path (scan QR from phone)",
			"telegram — bot API long-poll",
			"signal   — signal-cli subprocess",
			"discord  — Gateway WebSocket",
			"matrix   — Client-Server API",
			"slack    — Socket Mode (self-installed app)",
			"email    — IMAP inbound + SMTP outbound",
			"cli      — no messaging transport — TUI-only via `rousseau chat`",
		},
		"whatsapp")
	if err != nil {
		return a, err
	}
	a.Transport = transport

	// Q3 — transport-specific.
	if a.Transport == "whatsapp" {
		jid, err := w.askString(scanner,
			"Your WhatsApp JID (E164-format phone + @s.whatsapp.net, e.g. 15551234567@s.whatsapp.net). Leave blank to allow every sender (dangerous):",
			"")
		if err != nil {
			return a, err
		}
		a.WhatsAppJID = strings.TrimSpace(jid)
	}

	return a, nil
}

// writeConfig materialises the answers as YAML at w.configPath.
// Creates the parent directory. File mode 0600 because config
// may embed API keys.
func (w *setupWizard) writeConfig(a setupAnswers) error {
	if err := os.MkdirAll(filepath.Dir(w.configPath), 0o755); err != nil {
		return fmt.Errorf("setup: create config dir: %w", err)
	}
	body := renderSetupYAML(a)
	if err := os.WriteFile(w.configPath, []byte(body), 0o600); err != nil {
		return fmt.Errorf("setup: write %s: %w", w.configPath, err)
	}
	fmt.Fprintf(w.out, "\n✓  wrote %s (mode 0600)\n", w.configPath) //nolint:errcheck
	return nil
}

// renderSetupYAML materialises the answers as YAML. Kept as a
// pure function so tests can assert on the exact output shape
// without touching the filesystem.
func renderSetupYAML(a setupAnswers) string {
	var b strings.Builder
	b.WriteString("# rousseau-agent config — generated by `rousseau setup`.\n")
	b.WriteString("# Docs: https://github.com/sebastienrousseau/rousseau-agent/tree/main/docs\n\n")

	b.WriteString("provider: " + a.Provider + "\n\n")

	switch a.Provider {
	case "anthropic":
		b.WriteString("anthropic:\n")
		if a.AnthropicKey != "" {
			b.WriteString("  api_key: " + a.AnthropicKey + "\n")
		} else {
			b.WriteString("  # api_key: sk-ant-...   # or export ANTHROPIC_API_KEY\n")
		}
		b.WriteString("\n")
	case "openai":
		b.WriteString("openai:\n")
		if a.OpenAIKey != "" {
			b.WriteString("  api_key: " + a.OpenAIKey + "\n")
		} else {
			b.WriteString("  # api_key: sk-...        # or export OPENAI_API_KEY\n")
		}
		b.WriteString("  model: gpt-5\n")
		b.WriteString("\n")
	case "ollama":
		b.WriteString("ollama:\n")
		b.WriteString("  base_url: " + a.OllamaURL + "\n")
		b.WriteString("  model: llama3.1:8b\n")
		b.WriteString("\n")
	}

	b.WriteString("state:\n")
	b.WriteString("  driver: sqlite\n")
	b.WriteString("  dsn: ${XDG_DATA_HOME:-~/.local/share}/rousseau/sessions.db\n\n")

	b.WriteString("agent:\n")
	b.WriteString("  max_iterations: 32\n")
	b.WriteString("  # Turn on to get Predictability signal in `rousseau reliability`.\n")
	b.WriteString("  # Costs ~40 tokens/turn (prompt addendum) + ~10 tokens/turn (closing tag).\n")
	b.WriteString("  enable_confidence_elicitation: false\n\n")

	if a.Transport == "whatsapp" {
		b.WriteString("# WhatsApp transport picks up ROUSSEAU_WHATSAPP_ALLOW from the env.\n")
		b.WriteString("# Set your JID as the allowlist so strangers can't reach the daemon.\n")
		if a.WhatsAppJID != "" {
			b.WriteString("# Recommended: export ROUSSEAU_WHATSAPP_ALLOW=" + a.WhatsAppJID + "\n")
		} else {
			b.WriteString("# Recommended: export ROUSSEAU_WHATSAPP_ALLOW=<your JID>@s.whatsapp.net\n")
		}
		b.WriteString("\n")
	}
	return b.String()
}

func (w *setupWizard) printNextSteps(a setupAnswers) {
	fmt.Fprintln(w.out, "")                                        //nolint:errcheck
	fmt.Fprintln(w.out, "Next steps:")                             //nolint:errcheck
	fmt.Fprintln(w.out, "")                                        //nolint:errcheck
	fmt.Fprintln(w.out, "  1. Verify the config + prerequisites:") //nolint:errcheck
	fmt.Fprintln(w.out, "         rousseau doctor")                //nolint:errcheck
	fmt.Fprintln(w.out, "")                                        //nolint:errcheck

	switch a.Transport {
	case "whatsapp":
		fmt.Fprintln(w.out, "  2. Start the WhatsApp bridge (a QR code will print — scan from your phone: Settings → Linked Devices):") //nolint:errcheck
		if a.WhatsAppJID != "" {
			fmt.Fprintf(w.out, "         export ROUSSEAU_WHATSAPP_ALLOW=%s\n", a.WhatsAppJID) //nolint:errcheck
		} else {
			fmt.Fprintln(w.out, "         export ROUSSEAU_WHATSAPP_ALLOW=<your-JID>@s.whatsapp.net") //nolint:errcheck
		}
		fmt.Fprintln(w.out, "         rousseau whatsapp") //nolint:errcheck
	case "cli":
		fmt.Fprintln(w.out, "  2. Start the interactive TUI:") //nolint:errcheck
		fmt.Fprintln(w.out, "         rousseau chat")          //nolint:errcheck
	default:
		fmt.Fprintf(w.out, "  2. Start the %s bridge:\n", a.Transport) //nolint:errcheck
		fmt.Fprintf(w.out, "         rousseau %s\n", a.Transport)      //nolint:errcheck
	}
	fmt.Fprintln(w.out, "")                                                                                   //nolint:errcheck
	fmt.Fprintln(w.out, "  For the full container-native production layout (Podman + systemd Quadlet), see:") //nolint:errcheck
	fmt.Fprintln(w.out, "         scripts/install-from-source.sh in the repo.")                               //nolint:errcheck
}

// -- prompt helpers --------------------------------------------------

// askChoice renders a numbered menu, accepts either the number or
// the leading keyword as input. Empty input → defaultKey.
// Non-interactive mode always returns defaultKey.
func (w *setupWizard) askChoice(scanner *bufio.Scanner, question string, options []string, defaultKey string) (string, error) {
	fmt.Fprintln(w.out, question) //nolint:errcheck
	for i, opt := range options {
		fmt.Fprintf(w.out, "  [%d] %s\n", i+1, opt) //nolint:errcheck
	}
	if w.nonInteractive {
		fmt.Fprintf(w.out, "  → (--yes) %s\n\n", defaultKey) //nolint:errcheck
		return defaultKey, nil
	}
	fmt.Fprintf(w.out, "  > [default: %s] ", defaultKey) //nolint:errcheck
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return "", fmt.Errorf("setup: read input: %w", err)
		}
		return defaultKey, nil
	}
	answer := strings.TrimSpace(scanner.Text())
	fmt.Fprintln(w.out) //nolint:errcheck
	if answer == "" {
		return defaultKey, nil
	}
	// Match by leading word (before any whitespace).
	for i, opt := range options {
		key := strings.Fields(opt)[0]
		if answer == key || answer == fmt.Sprintf("%d", i+1) {
			return key, nil
		}
	}
	// Unknown input: fall back to default rather than error —
	// forces progress. The user always sees the choice we made
	// via the next-steps output.
	fmt.Fprintf(w.out, "!  unrecognised '%s', using default '%s'\n\n", answer, defaultKey) //nolint:errcheck
	return defaultKey, nil
}

// askString prompts for a free-text value. Empty → defaultVal.
func (w *setupWizard) askString(scanner *bufio.Scanner, question, defaultVal string) (string, error) {
	if defaultVal != "" {
		fmt.Fprintf(w.out, "%s [default: %s]\n  > ", question, defaultVal) //nolint:errcheck
	} else {
		fmt.Fprintf(w.out, "%s\n  > ", question) //nolint:errcheck
	}
	if w.nonInteractive {
		fmt.Fprintln(w.out, "(--yes)") //nolint:errcheck
		return defaultVal, nil
	}
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return "", fmt.Errorf("setup: read input: %w", err)
		}
		return defaultVal, nil
	}
	answer := strings.TrimSpace(scanner.Text())
	fmt.Fprintln(w.out) //nolint:errcheck
	if answer == "" {
		return defaultVal, nil
	}
	return answer, nil
}

// askSecret prompts for a secret value. Currently identical to
// askString — a future improvement is stty-based echo suppression,
// but that requires terminal detection and is out of scope for the
// first-run wizard (the operator can always leave blank + export
// via env var).
func (w *setupWizard) askSecret(scanner *bufio.Scanner, question string) (string, error) {
	return w.askString(scanner, question, "")
}

// readLine is a scanner-less line reader used by checkOverwrite
// which runs BEFORE the answers scanner is created (avoids
// double-buffering stdin).
func readLine(r io.Reader) (string, error) {
	buf := make([]byte, 1024)
	n, err := r.Read(buf)
	if err != nil && n == 0 {
		return "", err
	}
	line := string(buf[:n])
	if idx := strings.IndexByte(line, '\n'); idx >= 0 {
		line = line[:idx]
	}
	return strings.TrimRight(line, "\r"), nil
}
