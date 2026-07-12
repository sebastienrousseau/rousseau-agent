package cli

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

func newInitCmd(opts *Options) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Interactive first-run setup (writes ~/.config/rousseau/config.yaml)",
		Long: "Runs a short set of prompts, checks your environment, and\n" +
			"writes a starter config to $XDG_CONFIG_HOME/rousseau/config.yaml.\n" +
			"Existing configs are preserved unless --force is passed.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			w := cmd.OutOrStdout()
			in := bufio.NewReader(cmd.InOrStdin())
			return runInit(w, in, opts, force)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "overwrite an existing config file")
	return cmd
}

// runInit is the tested entry point; the cobra shell is a thin wrapper.
// Split out so tests can pipe scripted stdin and inspect the resulting
// config file without spawning cobra.
func runInit(w io.Writer, in *bufio.Reader, opts *Options, force bool) error {
	fmt.Fprintln(w, "rousseau init")
	fmt.Fprintln(w, "")

	// -- provider ------------------------------------------------------
	fmt.Fprintln(w, "Which provider should the agent use?")
	fmt.Fprintln(w, "  [1] claudecli  — shell out to the local `claude` CLI (default; no API key)")
	fmt.Fprintln(w, "  [2] anthropic  — direct Anthropic API")
	fmt.Fprintln(w, "  [3] openai     — OpenAI Chat Completions")
	fmt.Fprintln(w, "  [4] openrouter — any model via OpenRouter (OpenAI shim)")
	fmt.Fprintln(w, "  [5] ollama     — local ollama endpoint")
	fmt.Fprintln(w, "  [6] bedrock    — AWS Bedrock (Anthropic Claude via SigV4)")
	choice := prompt(w, in, "provider [1]: ", "1")
	providerName, providerBlock := pickProvider(choice, w, in)

	// -- workspace path ------------------------------------------------
	home, _ := os.UserHomeDir()
	workspaceDefault := filepath.Join(home, "team-rousseau-workspace")
	workspace := prompt(w, in, fmt.Sprintf("workspace path [%s]: ", workspaceDefault), workspaceDefault)

	// -- transports ----------------------------------------------------
	whatsappJID := prompt(w, in, "WhatsApp allowlist JID (blank to skip WhatsApp): ", "")
	telegramToken := prompt(w, in, "Telegram bot token (blank to skip Telegram): ", "")

	// -- write config --------------------------------------------------
	cfgDir := filepath.Join(home, ".config", "rousseau")
	cfgPath := filepath.Join(cfgDir, "config.yaml")
	if !force {
		if _, err := os.Stat(cfgPath); err == nil {
			return fmt.Errorf("%s already exists; pass --force to overwrite", cfgPath)
		}
	}
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		return err
	}
	content := renderConfig(providerName, providerBlock, workspace, whatsappJID, telegramToken)
	if err := os.WriteFile(cfgPath, []byte(content), 0o600); err != nil {
		return err
	}
	fmt.Fprintf(w, "\n✓ wrote %s\n\n", cfgPath)

	// -- next-steps summary -------------------------------------------
	writeNextSteps(w, providerName, whatsappJID, telegramToken)
	_ = opts // reserved for future validation logic
	return nil
}

func pickProvider(choice string, w io.Writer, in *bufio.Reader) (string, string) {
	switch strings.TrimSpace(choice) {
	case "2":
		key := prompt(w, in, "anthropic api_key (env ANTHROPIC_API_KEY if blank): ", "")
		model := prompt(w, in, "anthropic model [claude-sonnet-4-6]: ", "claude-sonnet-4-6")
		return "anthropic", fmt.Sprintf("anthropic:\n  api_key: %q\n  model: %q\n", key, model)
	case "3":
		key := prompt(w, in, "openai api_key: ", "")
		model := prompt(w, in, "openai model [gpt-4o-mini]: ", "gpt-4o-mini")
		return "openai", fmt.Sprintf("openai:\n  api_key: %q\n  model: %q\n", key, model)
	case "4":
		key := prompt(w, in, "openrouter api_key: ", "")
		model := prompt(w, in, "openrouter model [anthropic/claude-3.5-sonnet]: ", "anthropic/claude-3.5-sonnet")
		return "openrouter", fmt.Sprintf("openrouter:\n  api_key: %q\n  model: %q\n", key, model)
	case "5":
		model := prompt(w, in, "ollama model [qwen3-coder]: ", "qwen3-coder")
		return "ollama", fmt.Sprintf("ollama:\n  model: %q\n", model)
	case "6":
		region := prompt(w, in, "bedrock region [us-west-2]: ", "us-west-2")
		model := prompt(w, in, "bedrock model [anthropic.claude-sonnet-4-6-20260101-v1:0]: ", "anthropic.claude-sonnet-4-6-20260101-v1:0")
		return "bedrock", fmt.Sprintf("bedrock:\n  region: %q\n  model: %q\n", region, model)
	default:
		return "claudecli", fmt.Sprintf("claudecli:\n  binary: %q\n  permission_mode: bypassPermissions\n", claudeBinary())
	}
}

// prompt reads a line, returns fallback when the user just hits Enter.
func prompt(w io.Writer, in *bufio.Reader, question, fallback string) string {
	fmt.Fprint(w, question)
	line, _ := in.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		return fallback
	}
	return line
}

func claudeBinary() string {
	if p, err := exec.LookPath("claude"); err == nil {
		return p
	}
	return "claude"
}

// renderConfig produces the YAML body written to config.yaml.
func renderConfig(provider, providerBlock, workspace, whatsappJID, telegramToken string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "# rousseau-agent config (generated by `rousseau init`)\n\n")
	fmt.Fprintf(&sb, "provider: %s\n\n", provider)
	sb.WriteString(providerBlock)
	sb.WriteString("\n")
	if workspace != "" {
		fmt.Fprintf(&sb, "state:\n  path: %s/.local/share/rousseau/sessions.db\n\n", os.Getenv("HOME"))
	}
	if whatsappJID != "" {
		fmt.Fprintf(&sb, "whatsapp:\n  reply_header: \"💎 *Rousseau Agent*\\n\\n\"\n\n")
	}
	if telegramToken != "" {
		fmt.Fprintf(&sb, "telegram:\n  token: %q\n\n", telegramToken)
	}
	return sb.String()
}

func writeNextSteps(w io.Writer, provider, whatsappJID, telegramToken string) {
	fmt.Fprintln(w, "Next steps:")
	if whatsappJID != "" {
		fmt.Fprintf(w, "  1. `rousseau whatsapp --allow %s`  (first launch prints a QR code)\n", whatsappJID)
	} else if telegramToken != "" {
		fmt.Fprintln(w, "  1. `rousseau telegram`  (starts long-polling right away)")
	} else {
		fmt.Fprintln(w, "  1. `rousseau chat`  (interactive TUI)")
	}
	fmt.Fprintln(w, "  2. `rousseau status`  (verify state DB, cron jobs)")
	fmt.Fprintln(w, "  3. `rousseau doctor`  (full diagnostic sweep)")
	if provider == "claudecli" {
		fmt.Fprintln(w, "  - claude CLI must be on $PATH. If missing, install Claude Code first.")
	}
	fmt.Fprintln(w, "")
}
