// Package cli wires the Cobra command tree. Entry point lives in
// cmd/rousseau/main.go; this package is deliberately UI-thin so it
// remains testable.
package cli

import (
	"context"
	"errors"
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

// Version returns the stamped build version. Callers that need to
// thread the daemon's own version through to a subsystem (e.g. the
// MCP client's ClientInfo.Version) should read it here rather than
// hardcoding.
func Version() string { return version }

// Options bundles cross-command runtime state.
type Options struct {
	ConfigPath string
	Config     *config.Config
	Logger     *slog.Logger
	// AllowAnyone is --allow-anyone: the explicit opt-in to run a chat
	// transport with an empty sender allowlist.
	AllowAnyone bool
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
				// A config the operator must fix: exit 78 so systemd
				// stops the unit instead of crash-looping on it.
				return needsOperator(err)
			}
			opts.Config = cfg
			opts.Logger = newLogger(cfg.Log.Level, cfg.Log.Format, os.Stderr)
			return nil
		},
	}

	root.PersistentFlags().BoolVar(&opts.AllowAnyone, "allow-anyone", false, "run a chat transport with no sender allowlist (anyone who can message the account reaches the agent)")
	root.PersistentFlags().StringVar(&opts.ConfigPath, "config", "", "path to a config file (default: $XDG_CONFIG_HOME/rousseau/config.yaml)")

	root.AddCommand(newHookCmd())
	root.AddCommand(newHealthCmd())
	root.AddCommand(newChatCmd(opts))
	root.AddCommand(newWhatsAppCmd(opts))
	root.AddCommand(newDoctorCmd(opts))
	root.AddCommand(newSessionCmd(opts))
	root.AddCommand(newMigrateCmd(opts))
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
	root.AddCommand(newSetupCmd(opts))
	root.AddCommand(newReliabilityCmd(opts))
	root.AddCommand(newEvalCmd(opts))
	root.AddCommand(newA2ACmd(opts))
	root.AddCommand(newVersionCmd())
	return root
}

// Execute runs the root command with the process context.
func Execute(ctx context.Context) int {
	defer restrictUmask()()
	opts := &Options{}
	root := NewRoot(opts)
	err := root.ExecuteContext(ctx)
	var ec *exitCodeError
	if err != nil && (!errors.As(err, &ec) || !ec.silent) {
		fmt.Fprintln(os.Stderr, "error:", err)
	}
	return exitCodeFor(err)
}

// ExitNeedsOperator (EX_CONFIG) is the exit status for failures a
// restart cannot fix. Service units pair it with
// RestartPreventExitStatus=78 so the unit stops and shows as failed
// rather than looping.
const ExitNeedsOperator = 78

type exitCodeError struct {
	err    error
	code   int
	silent bool // the command already wrote its own stderr
}

// silentExit makes Execute exit with code without printing anything,
// for commands whose stderr is itself the protocol (claude hooks).
func silentExit(code int) error {
	return &exitCodeError{err: fmt.Errorf("exit %d", code), code: code, silent: true}
}

func (e *exitCodeError) Error() string { return e.err.Error() }
func (e *exitCodeError) Unwrap() error { return e.err }

// needsOperator tags err so Execute exits with ExitNeedsOperator
// instead of 1.
func needsOperator(err error) error {
	if err == nil {
		return nil
	}
	return &exitCodeError{err: err, code: ExitNeedsOperator}
}

func exitCodeFor(err error) int {
	if err == nil {
		return 0
	}
	var ec *exitCodeError
	if errors.As(err, &ec) {
		return ec.code
	}
	return 1
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
