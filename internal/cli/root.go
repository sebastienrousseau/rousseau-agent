// Package cli wires the Cobra command tree. Entry point lives in
// cmd/rousseau/main.go; this package is deliberately UI-thin so it
// remains testable.
package cli

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/sebastienrousseau/rousseau-agent/internal/config"
	"github.com/sebastienrousseau/rousseau-agent/internal/observability/redact"
)

var (
	// version is stamped by build tooling via -ldflags.
	version = "dev"
	// commit is stamped by build tooling via -ldflags.
	commit = "none"
	// buildDate is stamped by build tooling via -ldflags.
	buildDate = "unknown"
)

// Options bundles cross-command runtime state.
type Options struct {
	ConfigPath string
	Config     *config.Config
	Logger     *slog.Logger
}

// NewRoot constructs the root Cobra command.
func NewRoot(opts *Options) *cobra.Command {
	root := &cobra.Command{
		Use:   "rousseau",
		Short: "rousseau — a private, enterprise-grade coding assistant",
		Long: "rousseau is a coding assistant that runs in your terminal, powered by Anthropic Claude.\n" +
			"It ships a Bubble Tea TUI, a small tool registry, and durable session state.",
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(_ *cobra.Command, _ []string) error {
			cfg, err := config.Load(opts.ConfigPath)
			if err != nil {
				return err
			}
			opts.Config = cfg
			opts.Logger = newLogger(cfg.Log.Level, cfg.Log.Format, os.Stderr)
			return nil
		},
	}

	root.PersistentFlags().StringVar(&opts.ConfigPath, "config", "", "path to a config file (default: $XDG_CONFIG_HOME/rousseau/config.yaml)")

	root.AddCommand(newChatCmd(opts))
	root.AddCommand(newWhatsAppCmd(opts))
	root.AddCommand(newDoctorCmd(opts))
	root.AddCommand(newSessionCmd(opts))
	root.AddCommand(newCronCmd(opts))
	root.AddCommand(newMCPCmd(opts))
	root.AddCommand(newSkillsCmd(opts))
	root.AddCommand(newSignalCmd(opts))
	root.AddCommand(newTelegramCmd(opts))
	root.AddCommand(newMatrixCmd(opts))
	root.AddCommand(newSlackCmd(opts))
	root.AddCommand(newDiscordCmd(opts))
	root.AddCommand(newSMSCmd(opts))
	root.AddCommand(newIMessageCmd(opts))
	root.AddCommand(newEmailCmd(opts))
	root.AddCommand(newStatusCmd(opts))
	root.AddCommand(newInitCmd(opts))
	root.AddCommand(newVersionCmd())
	return root
}

// Execute runs the root command with the process context.
func Execute(ctx context.Context) int {
	opts := &Options{}
	root := NewRoot(opts)
	if err := root.ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	return 0
}

func newLogger(level, format string, w io.Writer) *slog.Logger {
	var lvl slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn", "warning":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	handlerOpts := &slog.HandlerOptions{Level: lvl}
	var h slog.Handler
	if strings.EqualFold(format, "json") {
		h = slog.NewJSONHandler(w, handlerOpts)
	} else {
		h = slog.NewTextHandler(w, handlerOpts)
	}
	if os.Getenv(envLogNoRedact) != "1" {
		rules := redact.DefaultRules()
		if os.Getenv(envLogRedactPhones) == "1" {
			rules = append(rules, redact.PhoneRule())
		}
		h = redact.New(h, rules)
	}
	return slog.New(h)
}

// envLogNoRedact opts out of the redacting slog handler; intended for
// local debugging only.
const envLogNoRedact = "ROUSSEAU_LOG_NO_REDACT"

// envLogRedactPhones opts the phone-number rule in on top of the
// default rule set.
const envLogRedactPhones = "ROUSSEAU_LOG_REDACT_PHONES"
