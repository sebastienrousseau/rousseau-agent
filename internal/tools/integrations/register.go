// Package integrations groups the native tool suites (GitHub,
// Slack, Google Workspace, Linear, Stripe). Top-level RegisterAll
// wires every enabled suite into the tool registry in one call.
package integrations

import (
	"context"
	"fmt"
	"log/slog"

	//nolint:gci // grouped by function, not path
	"github.com/sebastienrousseau/rousseau-agent/internal/tools"

	"github.com/sebastienrousseau/rousseau-agent/internal/tools/integrations/composio"
	"github.com/sebastienrousseau/rousseau-agent/internal/tools/integrations/github"
	"github.com/sebastienrousseau/rousseau-agent/internal/tools/integrations/google"
	"github.com/sebastienrousseau/rousseau-agent/internal/tools/integrations/linear"
	"github.com/sebastienrousseau/rousseau-agent/internal/tools/integrations/slack"
	"github.com/sebastienrousseau/rousseau-agent/internal/tools/integrations/stripe"
)

// Config groups the per-suite configs. A zero-value entry disables
// that suite; wiring is opt-in per suite.
type Config struct {
	GitHub   GitHubConfig
	Slack    SlackConfig
	Google   GoogleConfig
	Linear   LinearConfig
	Stripe   StripeConfig
	Composio ComposioConfig
}

// ComposioConfig toggles + configures the Composio-brokered
// tool-provider adapter. Opt-in: enabling it registers every action
// the operator has authorised in the Composio console.
type ComposioConfig struct {
	Enabled bool
	APIKey  string
	UserID  string
	// Apps restricts registration to the named Composio apps
	// (case-insensitive). Empty registers every visible action —
	// convenient for exploration, dangerous for auditing.
	Apps []string
}

// GitHubConfig toggles + configures the GitHub suite.
type GitHubConfig struct {
	Enabled bool
	Token   string
	BaseURL string
}

// SlackConfig toggles + configures the Slack tool suite.
type SlackConfig struct {
	Enabled  bool
	BotToken string
}

// GoogleConfig toggles + configures the Google Workspace suite.
type GoogleConfig struct {
	Enabled bool
	// TokenFn provides a fresh access token per request. The daemon
	// typically supplies a closure that calls the OAuth broker's
	// Load(ctx, "google", accountID).AccessToken.
	TokenFn func(ctx context.Context) (string, error)
}

// LinearConfig toggles + configures the Linear suite.
type LinearConfig struct {
	Enabled bool
	APIKey  string
}

// StripeConfig toggles + configures the Stripe read-only suite.
type StripeConfig struct {
	Enabled   bool
	SecretKey string
}

// RegisterAll wires every enabled suite into reg. Errors are
// reported per-suite; the first failure returns without touching the
// remaining suites so operators see the root cause on daemon startup.
func RegisterAll(reg *tools.Registry, cfg Config, logger *slog.Logger) error {
	if logger == nil {
		logger = slog.Default()
	}

	if cfg.GitHub.Enabled {
		c, err := github.New(github.Config{Token: cfg.GitHub.Token, BaseURL: cfg.GitHub.BaseURL})
		if err != nil {
			return fmt.Errorf("integrations/github: %w", err)
		}
		if err := github.Register(reg, c); err != nil {
			return err
		}
		logger.Info("integrations.registered", slog.String("suite", "github"))
	}

	if cfg.Slack.Enabled {
		c, err := slack.New(slack.Config{BotToken: cfg.Slack.BotToken})
		if err != nil {
			return fmt.Errorf("integrations/slack: %w", err)
		}
		if err := slack.Register(reg, c); err != nil {
			return err
		}
		logger.Info("integrations.registered", slog.String("suite", "slack"))
	}

	if cfg.Google.Enabled {
		c, err := google.New(google.Config{TokenFn: cfg.Google.TokenFn})
		if err != nil {
			return fmt.Errorf("integrations/google: %w", err)
		}
		if err := google.Register(reg, c); err != nil {
			return err
		}
		logger.Info("integrations.registered", slog.String("suite", "google"))
	}

	if cfg.Linear.Enabled {
		c, err := linear.New(linear.Config{APIKey: cfg.Linear.APIKey})
		if err != nil {
			return fmt.Errorf("integrations/linear: %w", err)
		}
		if err := linear.Register(reg, c); err != nil {
			return err
		}
		logger.Info("integrations.registered", slog.String("suite", "linear"))
	}

	if cfg.Stripe.Enabled {
		c, err := stripe.New(stripe.Config{SecretKey: cfg.Stripe.SecretKey})
		if err != nil {
			return fmt.Errorf("integrations/stripe: %w", err)
		}
		if err := stripe.Register(reg, c); err != nil {
			return err
		}
		logger.Info("integrations.registered", slog.String("suite", "stripe"))
	}

	if cfg.Composio.Enabled {
		c, err := composio.New(composio.Config{
			APIKey: cfg.Composio.APIKey,
			UserID: cfg.Composio.UserID,
		})
		if err != nil {
			return fmt.Errorf("integrations/composio: %w", err)
		}
		n, err := composio.Register(context.Background(), reg, c, cfg.Composio.Apps)
		if err != nil {
			return fmt.Errorf("integrations/composio: %w", err)
		}
		logger.Info("integrations.registered",
			slog.String("suite", "composio"),
			slog.Int("action_count", n))
	}

	return nil
}
