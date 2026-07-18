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
	"log/slog"
	"os"

	"github.com/sebastienrousseau/rousseau-agent/pkg/tools"
	"github.com/sebastienrousseau/rousseau-agent/pkg/tools/integrations"
)

func main() {
	reg := tools.NewRegistry()

	cfg := integrations.Config{
		// Every suite is opt-in. Set Enabled=true + Token/APIKey/
		// SecretKey to register that suite's tools. Missing credentials
		// short-circuit the whole daemon startup with a clear error.
		GitHub: integrations.GitHubConfig{Enabled: os.Getenv("ROUSSEAU_GITHUB_TOKEN") != ""},
		Slack:  integrations.SlackConfig{Enabled: os.Getenv("ROUSSEAU_SLACK_TOOLS_TOKEN") != ""},
		Linear: integrations.LinearConfig{Enabled: os.Getenv("ROUSSEAU_LINEAR_API_KEY") != ""},
		Stripe: integrations.StripeConfig{Enabled: os.Getenv("ROUSSEAU_STRIPE_KEY") != ""},
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	if err := integrations.RegisterAll(reg, cfg, logger); err != nil {
		fmt.Fprintf(os.Stderr, "register: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("registered %d tool(s):\n", len(reg.Names()))
	for _, name := range reg.Names() {
		fmt.Printf("  %s\n", name)
	}
}
