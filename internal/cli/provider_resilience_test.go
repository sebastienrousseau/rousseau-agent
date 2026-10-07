package cli

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
	"github.com/sebastienrousseau/rousseau-agent/internal/config"
	"github.com/sebastienrousseau/rousseau-agent/internal/resilience"
)

type plainProvider struct{}

func (plainProvider) Name() string { return "plain" }
func (plainProvider) Complete(context.Context, agent.Request) (agent.Response, error) {
	return agent.Response{}, nil
}

func TestWrapResilience(t *testing.T) {
	t.Run("claudecli and router pass through", func(t *testing.T) {
		for _, name := range []string{"", "claudecli", "router"} {
			cfg := &config.Config{Provider: name}
			_, same := wrapResilience(plainProvider{}, cfg).(plainProvider)
			assert.True(t, same, "provider %q must not be wrapped", name)
		}
	})
	t.Run("API providers get retries by default", func(t *testing.T) {
		cfg := &config.Config{Provider: "anthropic"}
		_, isRetry := wrapResilience(plainProvider{}, cfg).(*resilience.RetryProvider)
		assert.True(t, isRetry)
	})
	t.Run("max_attempts 1 disables retries", func(t *testing.T) {
		cfg := &config.Config{Provider: "openai", Resilience: config.ResilienceConfig{Retry: config.RetryConfig{MaxAttempts: 1}}}
		_, same := wrapResilience(plainProvider{}, cfg).(plainProvider)
		assert.True(t, same)
	})
	t.Run("breaker is wired only when max_failures is set", func(t *testing.T) {
		cfg := &config.Config{Provider: "anthropic", Resilience: config.ResilienceConfig{
			CircuitBreaker: config.CircuitBreakerConfig{MaxFailures: 2},
			Retry:          config.RetryConfig{MaxAttempts: 1},
		}}
		_, isBreaker := wrapResilience(plainProvider{}, cfg).(*resilience.BreakerProvider)
		assert.True(t, isBreaker)
	})
	t.Run("the real anthropic provider keeps streaming behind both wrappers", func(t *testing.T) {
		cfg := &config.Config{Provider: "anthropic",
			Anthropic:  config.AnthropicConfig{APIKey: "sk-test", Model: "claude"},
			Resilience: config.ResilienceConfig{CircuitBreaker: config.CircuitBreakerConfig{MaxFailures: 3}},
		}
		p, err := buildProvider(cfg)
		require.NoError(t, err)
		_, streams := p.(agent.StreamingProvider)
		assert.True(t, streams)
		assert.Equal(t, "anthropic", p.Name())
	})
}
