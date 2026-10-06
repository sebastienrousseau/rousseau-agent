package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/sebastienrousseau/rousseau-agent/internal/config"
)

// newConfigCmd groups configuration inspection commands. Loading is
// already strict (unknown keys, type mismatches, unset ${VAR}
// references and a missing explicit --config are all errors), so
// `config validate` is the explicit, scriptable form of "the daemon
// would accept this file": it reports the resolved path and the
// effective top-level choices, and exits 78 through the shared
// PersistentPreRunE when the file is broken.
func newConfigCmd(opts *Options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Inspect the resolved configuration",
		Args:  cobra.NoArgs,
	}
	cmd.AddCommand(newConfigValidateCmd(opts))
	return cmd
}

// configSummary is what `config validate` prints: enough to confirm
// which file was used and which subsystems are on, nothing secret.
type configSummary struct {
	Path           string   `json:"path"`
	Provider       string   `json:"provider"`
	StateDriver    string   `json:"state_driver"`
	Transports     []string `json:"transports_configured"`
	MCPClients     int      `json:"mcp_clients"`
	AuditEgress    string   `json:"audit_egress_kind"`
	BashSandbox    string   `json:"bash_sandbox_kind"`
	WorkspaceRoot  string   `json:"tools_fs_root"`
	UnknownKeysOff bool     `json:"strict_decoding"`
}

func newConfigValidateCmd(opts *Options) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "validate",
		Short: "Load the config the way the daemon does and report what it resolved to",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg := opts.Config
			if cfg == nil {
				return fmt.Errorf("config: nothing loaded")
			}
			s := configSummary{
				Path:           resolvedConfigPath(opts.ConfigPath),
				Provider:       cfg.Provider,
				StateDriver:    driverName(cfg.State),
				MCPClients:     len(cfg.MCP.Clients),
				AuditEgress:    cfg.Observability.AuditEgress.Kind,
				BashSandbox:    cfg.Tools.Bash.Sandbox.Kind,
				WorkspaceRoot:  cfg.Tools.FS.Root,
				UnknownKeysOff: os.Getenv("ROUSSEAU_CONFIG_ALLOW_UNKNOWN") != "1",
			}
			if s.BashSandbox == "" {
				s.BashSandbox = "none"
			}
			s.Transports = configuredTransports(cfg)
			w := cmd.OutOrStdout()
			if asJSON {
				enc := json.NewEncoder(w)
				enc.SetIndent("", "  ")
				return enc.Encode(s)
			}
			fmt.Fprintf(w, "config: %s\n", s.Path)                              //nolint:errcheck // CLI output
			fmt.Fprintf(w, "  provider:         %s\n", s.Provider)              //nolint:errcheck // CLI output
			fmt.Fprintf(w, "  state driver:     %s\n", s.StateDriver)           //nolint:errcheck // CLI output
			fmt.Fprintf(w, "  transports:       %v\n", s.Transports)            //nolint:errcheck // CLI output
			fmt.Fprintf(w, "  mcp clients:      %d\n", s.MCPClients)            //nolint:errcheck // CLI output
			fmt.Fprintf(w, "  audit egress:     %s\n", orNone(s.AuditEgress))   //nolint:errcheck // CLI output
			fmt.Fprintf(w, "  bash sandbox:     %s\n", s.BashSandbox)           //nolint:errcheck // CLI output
			fmt.Fprintf(w, "  tools.fs.root:    %s\n", orNone(s.WorkspaceRoot)) //nolint:errcheck // CLI output
			fmt.Fprintf(w, "  strict decoding:  %t\n", s.UnknownKeysOff)        //nolint:errcheck // CLI output
			fmt.Fprintln(w, "OK")                                               //nolint:errcheck // CLI output
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit the summary as JSON")
	return cmd
}

// configuredTransports lists the chat transports that have the one
// setting each needs to start, so an operator can see at a glance
// which bridges this config would run.
func configuredTransports(cfg *config.Config) []string {
	var out []string
	add := func(name string, on bool) {
		if on {
			out = append(out, name)
		}
	}
	// WhatsApp needs no config key (the pairing store lives under the
	// state dir), so it is always startable and not listed here.
	add("signal", cfg.Signal.Account != "")
	add("telegram", cfg.Telegram.Token != "")
	add("discord", cfg.Discord.Token != "")
	add("slack", cfg.Slack.BotToken != "")
	add("matrix", cfg.Matrix.HomeserverURL != "")
	add("imessage", cfg.IMessage.BaseURL != "")
	add("email", cfg.Email.IMAPAddr != "")
	add("sms", cfg.SMS.Provider != "")
	if out == nil {
		out = []string{}
	}
	return out
}

func orNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}

// resolvedConfigPath mirrors config.Load's default-path rule for the
// report; it does not read the file.
func resolvedConfigPath(explicit string) string {
	if explicit != "" {
		return explicit
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "(default path unavailable)"
	}
	p := filepath.Join(home, ".config", "rousseau", "config.yaml")
	if _, err := os.Stat(p); err != nil {
		return p + " (absent, defaults in effect)"
	}
	return p
}
