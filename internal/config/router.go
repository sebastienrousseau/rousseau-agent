package config

// RouterConfig configures the multi-model routing provider (Provider =
// "router"). Named children under Providers are themselves any of the
// supported provider kinds; Rules pick a child per request. See
// [internal/llm/router] for evaluation semantics.
//
// Example:
//
//	provider: router
//	router:
//	  default: sonnet
//	  rules:
//	    - if:  { message_len_max: 200, tool_use_count_max: 0 }
//	      use: haiku
//	    - if:  { tool_use_count_min: 3 }
//	      use: opus
//	  providers:
//	    haiku:  { kind: anthropic, api_key: ${ANTHROPIC_API_KEY}, model: claude-haiku-4-5 }
//	    sonnet: { kind: anthropic, api_key: ${ANTHROPIC_API_KEY}, model: claude-sonnet-4-6 }
//	    opus:   { kind: anthropic, api_key: ${ANTHROPIC_API_KEY}, model: claude-opus-4-6 }
type RouterConfig struct {
	// Default names the provider key used when no rule matches.
	Default string `mapstructure:"default"`
	// Rules is the ordered list of routing decisions (first match wins).
	Rules []RouterRuleConfig `mapstructure:"rules"`
	// Providers maps a key (referenced by Rules.Use and Default) to
	// its concrete child-provider config.
	Providers map[string]RouterChildConfig `mapstructure:"providers"`
	// Classifier names the provider key (in Providers) that labels
	// messages for rules with intents. Required when any rule sets
	// intents; one extra small call per request.
	Classifier string `mapstructure:"classifier"`
}

// RouterRuleConfig is one routing rule. Empty match fields disable the
// corresponding filter; all set filters are AND'd.
type RouterRuleConfig struct {
	Name            string `mapstructure:"name"`
	MessageLenMax   int    `mapstructure:"message_len_max"`
	MessageLenMin   int    `mapstructure:"message_len_min"`
	ToolUseCountMax int    `mapstructure:"tool_use_count_max"`
	ToolUseCountMin int    `mapstructure:"tool_use_count_min"`
	SessionIDPrefix string `mapstructure:"session_id_prefix"`
	// Intents routes by what the message is about, e.g.
	// [smalltalk] to a cheap model; see router.classifier.
	Intents []string `mapstructure:"intents"`
	Use     string   `mapstructure:"use"`
}

// RouterChildConfig configures one child provider under
// router.providers. Fields are a union across the supported provider
// kinds — only the ones relevant to Kind are consulted.
type RouterChildConfig struct {
	Kind      string `mapstructure:"kind"` // anthropic | openai | openrouter | ollama | bedrock | vertex
	APIKey    string `mapstructure:"api_key"`
	BaseURL   string `mapstructure:"base_url"`
	Model     string `mapstructure:"model"`
	MaxTokens int64  `mapstructure:"max_tokens"`
	// bedrock-specific
	Region  string `mapstructure:"region"`
	Profile string `mapstructure:"profile"`
	// vertex-specific
	Project         string `mapstructure:"project"`
	CredentialsFile string `mapstructure:"credentials_file"`
}
