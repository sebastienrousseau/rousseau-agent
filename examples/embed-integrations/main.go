// Package main demonstrates wiring the native tool-integration
// suites (github + slack + linear + stripe + composio) into a tool
// registry. Every suite is opt-in via its Enabled flag; a suite
// without credentials is silently skipped.
//
// Run with:
//
//	ROUSSEAU_GITHUB_TOKEN=ghp_...  \
//	  go run ./examples/embed-integrations
package main

import (
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/sebastienrousseau/rousseau-agent/pkg/tools"
	"github.com/sebastienrousseau/rousseau-agent/pkg/tools/integrations"
)

func main() { os.Exit(run(os.Stdout, os.Stderr, configFromEnv())) }

// configFromEnv turns the ambient environment into a suite config.
// Every suite is opt-in. Set Enabled=true + Token/APIKey/SecretKey to
// register that suite's tools; each client also falls back to its own
// environment variable. Missing credentials short-circuit the whole
// daemon startup with a clear error.
func configFromEnv() integrations.Config {
	return integrations.Config{
		GitHub: integrations.GitHubConfig{Enabled: os.Getenv("ROUSSEAU_GITHUB_TOKEN") != ""},
		Slack:  integrations.SlackConfig{Enabled: os.Getenv("ROUSSEAU_SLACK_TOOLS_TOKEN") != ""},
		Linear: integrations.LinearConfig{Enabled: os.Getenv("ROUSSEAU_LINEAR_API_KEY") != ""},
		Stripe: integrations.StripeConfig{Enabled: os.Getenv("ROUSSEAU_STRIPE_KEY") != ""},
	}
}

// run registers the enabled suites and lists what the model can now
// reach for, returning the process exit code. main does nothing but
// call os.Exit so that tests can drive run directly.
func run(out, errOut io.Writer, cfg integrations.Config) int {
	reg := tools.NewRegistry()

	logger := slog.New(slog.NewTextHandler(out, nil))
	if err := integrations.RegisterAll(reg, cfg, logger); err != nil {
		fmt.Fprintf(errOut, "register: %v\n", err)
		return 1
	}

	fmt.Fprintf(out, "registered %d tool(s):\n", len(reg.Names()))
	for _, name := range reg.Names() {
		fmt.Fprintf(out, "  %s\n", name)
	}
	return 0
}
