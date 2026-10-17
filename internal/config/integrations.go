package config

// MCPConfig configures external MCP servers the agent should consume.
// Each entry under Clients spawns a subprocess at daemon start, walks
// its tools/list, and registers every discovered tool with the agent's
// [tools.Registry] under the name "mcp:<name>:<tool>".
//
// Example config.yaml:
//
//	mcp:
//	  clients:
//	    github:
//	      command: npx
//	      args: ['-y', '@modelcontextprotocol/server-github']
//	      env:
//	        GITHUB_PERSONAL_ACCESS_TOKEN: ${GITHUB_TOKEN}
//	    playwright:
//	      command: npx
//	      args: ['-y', '@modelcontextprotocol/server-playwright']
type MCPConfig struct {
	// Clients maps a server name (used as the tool-name prefix) to its
	// subprocess spec. Empty or absent map means no MCP clients are
	// started — the agent runs with local tools only.
	Clients map[string]MCPClientConfig `mapstructure:"clients"`
}

// MCPClientConfig describes one external MCP server subprocess.
type MCPClientConfig struct {
	// Command is the executable to spawn (resolved via $PATH).
	Command string `mapstructure:"command"`
	// Args are the command-line arguments passed to Command.
	Args []string `mapstructure:"args"`
	// Env are extra environment variables layered on top of the
	// scrubbed baseline environment. Set an entry to "" to unset a
	// variable.
	Env map[string]string `mapstructure:"env"`
	// EnvPassthrough names extra daemon environment variables (or
	// "NAME*" prefixes) the server may inherit. The daemon's secrets
	// are never inherited implicitly.
	EnvPassthrough []string `mapstructure:"env_passthrough"`
	// StartTimeoutSeconds bounds the initialize-handshake window.
	// Zero uses the client default (30s).
	StartTimeoutSeconds int `mapstructure:"start_timeout_seconds"`
	// RequestTimeoutSeconds bounds each tools/list and tools/call
	// invocation. Zero uses the client default (60s).
	RequestTimeoutSeconds int `mapstructure:"request_timeout_seconds"`
}

// IntegrationsConfig groups the native tool-integration suites +
// the Composio adapter (§1 of docs/IMPLEMENTATION_PLAN_2026_07_16.md).
// Every field is opt-in via `enabled: true` under its section.
type IntegrationsConfig struct {
	GitHub   GitHubToolsConfig   `mapstructure:"github"`
	Slack    SlackToolsConfig    `mapstructure:"slack"`
	Google   GoogleToolsConfig   `mapstructure:"google"`
	Linear   LinearToolsConfig   `mapstructure:"linear"`
	Stripe   StripeToolsConfig   `mapstructure:"stripe"`
	Composio ComposioToolsConfig `mapstructure:"composio"`
}

// GitHubToolsConfig configures the native GitHub tool suite.
type GitHubToolsConfig struct {
	Enabled bool   `mapstructure:"enabled"`
	Token   string `mapstructure:"token"`
	BaseURL string `mapstructure:"base_url"`
}

// SlackToolsConfig configures the native Slack tool suite (distinct
// from the SlackConfig transport — a bot token may be shared or
// separate).
type SlackToolsConfig struct {
	Enabled  bool   `mapstructure:"enabled"`
	BotToken string `mapstructure:"bot_token"`
}

// GoogleToolsConfig configures Gmail + Calendar + Drive.
type GoogleToolsConfig struct {
	Enabled bool `mapstructure:"enabled"`
}

// LinearToolsConfig configures the Linear GraphQL tools.
type LinearToolsConfig struct {
	Enabled bool   `mapstructure:"enabled"`
	APIKey  string `mapstructure:"api_key"`
}

// StripeToolsConfig configures the read-only Stripe tools.
type StripeToolsConfig struct {
	Enabled   bool   `mapstructure:"enabled"`
	SecretKey string `mapstructure:"secret_key"`
}

// ComposioToolsConfig configures the Composio adapter — registers
// every action visible to the authenticated user, or a subset
// filtered by Apps.
type ComposioToolsConfig struct {
	Enabled bool     `mapstructure:"enabled"`
	APIKey  string   `mapstructure:"api_key"`
	UserID  string   `mapstructure:"user_id"`
	Apps    []string `mapstructure:"apps"`
}
