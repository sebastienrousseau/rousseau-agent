// Package integrations re-exports the internal/tools/integrations
// registration surface so external modules can wire the native tool
// suites (GitHub, Slack, Google Workspace, Linear, Stripe, Composio)
// into a [tools.Registry] without importing /internal.
package integrations

import (
	"github.com/sebastienrousseau/rousseau-agent/internal/tools/integrations"
)

// Config aliases [integrations.Config].
type Config = integrations.Config

// GitHubConfig aliases [integrations.GitHubConfig].
type GitHubConfig = integrations.GitHubConfig

// SlackConfig aliases [integrations.SlackConfig].
type SlackConfig = integrations.SlackConfig

// GoogleConfig aliases [integrations.GoogleConfig].
type GoogleConfig = integrations.GoogleConfig

// LinearConfig aliases [integrations.LinearConfig].
type LinearConfig = integrations.LinearConfig

// StripeConfig aliases [integrations.StripeConfig].
type StripeConfig = integrations.StripeConfig

// ComposioConfig aliases [integrations.ComposioConfig].
type ComposioConfig = integrations.ComposioConfig

// RegisterAll wires every enabled suite into the registry. Alias for
// [integrations.RegisterAll].
var RegisterAll = integrations.RegisterAll
