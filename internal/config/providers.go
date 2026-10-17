package config

// VertexConfig configures the Google Vertex AI provider (Anthropic on
// Vertex).
type VertexConfig struct {
	Project         string `mapstructure:"project"`
	Region          string `mapstructure:"region"`
	Model           string `mapstructure:"model"`
	CredentialsFile string `mapstructure:"credentials_file"`
	MaxTokens       int64  `mapstructure:"max_tokens"`
}

// BedrockConfig configures the AWS Bedrock provider.
type BedrockConfig struct {
	Region    string `mapstructure:"region"`
	Model     string `mapstructure:"model"`
	Profile   string `mapstructure:"profile"`
	MaxTokens int64  `mapstructure:"max_tokens"`
}

// OpenAIConfig configures the OpenAI-compatible provider. Shared by
// openai / openrouter / ollama / other OpenAI-shim endpoints.
type OpenAIConfig struct {
	APIKey    string `mapstructure:"api_key"`
	Model     string `mapstructure:"model"`
	BaseURL   string `mapstructure:"base_url"`
	MaxTokens int64  `mapstructure:"max_tokens"`
}

// AnthropicConfig configures the direct Anthropic API provider.
type AnthropicConfig struct {
	APIKey    string `mapstructure:"api_key"`
	Model     string `mapstructure:"model"`
	MaxTokens int64  `mapstructure:"max_tokens"`
}

// ClaudeCLIConfig configures the claudecli (subprocess) provider.
type ClaudeCLIConfig struct {
	// Binary is the claude executable. Empty defaults to "claude".
	Binary string `mapstructure:"binary"`
	// Model overrides claude's default.
	Model string `mapstructure:"model"`
	// PermissionMode is claude's --permission-mode
	// (acceptEdits, auto, bypassPermissions, default, dontAsk, plan).
	// Unattended daemons (whatsapp) generally need "bypassPermissions".
	PermissionMode string `mapstructure:"permission_mode"`
	// Bare passes --bare to claude, which skips CLAUDE.md auto-discovery,
	// hooks, LSP, plugin sync, auto-memory, background prefetches, and
	// keychain reads. Cuts per-invocation cold-start latency from minutes
	// (walking a large mounted workspace) to seconds. Trade-off:
	// authentication becomes strictly ANTHROPIC_API_KEY / apiKeyHelper
	// (no OAuth / keychain). Recommended for unattended bridge daemons
	// (whatsapp) where the workspace scan is pure overhead per message.
	Bare bool `mapstructure:"bare"`
	// ExtraArgs are appended to every invocation.
	ExtraArgs []string `mapstructure:"extra_args"`
	// EnvPassthrough names extra environment variables (or "NAME*"
	// prefixes) the claude child may inherit. The child gets a scrubbed
	// environment: the envscrub baseline, ANTHROPIC_*, CLAUDE_*, proxy
	// and CA settings. Bedrock/Vertex through claude needs AWS_* or
	// GOOGLE_* here.
	EnvPassthrough []string `mapstructure:"env_passthrough"`
	// DisablePolicyHook turns off the toolgate bridge. By default the
	// daemon installs a PreToolUse hook so every tool claude runs goes
	// through rousseau's approver and audit trail. Only disable it if
	// you govern claude's tools by other means; the bridge cannot run
	// alongside Bare (claude --bare skips hooks), so Bare requires it.
	DisablePolicyHook bool `mapstructure:"disable_policy_hook"`
}
