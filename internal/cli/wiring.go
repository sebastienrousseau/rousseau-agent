package cli

import (
	"fmt"

	"github.com/sebastienrousseau/rousseau-agent/internal/config"
	"github.com/sebastienrousseau/rousseau-agent/internal/ratelimit"
	"github.com/sebastienrousseau/rousseau-agent/internal/tools/integrations"
)

// integrationsFromConfig lifts the tool-integration block out of a
// [config.Config] and shapes it as an [integrations.Config]. Kept in
// its own file so daemon.go stays focused on assembly, not on
// per-suite key mapping.
//
// Every suite is opt-in via `enabled: true` under its section; a
// missing section leaves the suite disabled.
func integrationsFromConfig(cfg *config.Config) integrations.Config {
	if cfg == nil {
		return integrations.Config{}
	}
	c := cfg.Integrations
	return integrations.Config{
		GitHub: integrations.GitHubConfig{
			Enabled: c.GitHub.Enabled,
			Token:   c.GitHub.Token,
			BaseURL: c.GitHub.BaseURL,
		},
		Slack: integrations.SlackConfig{
			Enabled:  c.Slack.Enabled,
			BotToken: c.Slack.BotToken,
		},
		Google: integrations.GoogleConfig{
			Enabled: c.Google.Enabled,
			// TokenFn is wired by the caller when the OAuth broker is
			// available. When left nil the google.New constructor will
			// fall back to Config.AccessToken, which the config surface
			// does not currently expose — an operator who enables
			// Google without also wiring the broker sees a startup
			// error, which is the right behaviour.
			TokenFn: nil,
		},
		Linear: integrations.LinearConfig{
			Enabled: c.Linear.Enabled,
			APIKey:  c.Linear.APIKey,
		},
		Stripe: integrations.StripeConfig{
			Enabled:   c.Stripe.Enabled,
			SecretKey: c.Stripe.SecretKey,
		},
		Composio: integrations.ComposioConfig{
			Enabled: c.Composio.Enabled,
			APIKey:  c.Composio.APIKey,
			UserID:  c.Composio.UserID,
			Apps:    c.Composio.Apps,
		},
	}
}

// buildRateLimiters constructs one [ratelimit.KeyedLimiter] per
// transport based on the merged default + per-transport policy. An
// empty config returns a nil map — callers treat that as "no rate
// limiting" and skip the Wrap step.
func buildRateLimiters(cfg config.RateLimitConfig) (map[string]*ratelimit.KeyedLimiter, error) {
	if cfg.Default == "" && len(cfg.PerTransport) == 0 {
		return nil, nil
	}
	out := map[string]*ratelimit.KeyedLimiter{}
	var defaultRate ratelimit.Rate
	if cfg.Default != "" {
		r, err := ratelimit.ParseRate(cfg.Default)
		if err != nil {
			return nil, fmt.Errorf("ratelimit: default: %w", err)
		}
		defaultRate = r
	}
	// Every known transport gets an entry so lookup is O(1) at the
	// call site. Unknown transport names in PerTransport are still
	// added — no harm, and useful when a new transport lands.
	known := []string{
		"whatsapp", "signal", "telegram", "matrix",
		"slack", "discord", "sms", "imessage", "email",
	}
	for _, name := range known {
		rate := defaultRate
		if s, ok := cfg.PerTransport[name]; ok {
			r, err := ratelimit.ParseRate(s)
			if err != nil {
				return nil, fmt.Errorf("ratelimit: %s: %w", name, err)
			}
			rate = r
		}
		if rate.Requests == 0 {
			continue
		}
		out[name] = ratelimit.NewKeyedLimiter(rate.Requests, rate.RefillPerSec(), cfg.MaxKeys)
	}
	// Also honour custom transports the operator names in
	// per_transport but that aren't in the known list.
	for name, s := range cfg.PerTransport {
		if _, ok := out[name]; ok {
			continue
		}
		r, err := ratelimit.ParseRate(s)
		if err != nil {
			return nil, fmt.Errorf("ratelimit: %s: %w", name, err)
		}
		out[name] = ratelimit.NewKeyedLimiter(r.Requests, r.RefillPerSec(), cfg.MaxKeys)
	}
	return out, nil
}
