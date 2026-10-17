package config

// SMSConfig configures the Twilio/Vonage SMS transport.
type SMSConfig struct {
	Provider    string `mapstructure:"provider"` // "twilio" or "vonage"
	From        string `mapstructure:"from"`
	AccountSID  string `mapstructure:"account_sid"` // Twilio
	AuthToken   string `mapstructure:"auth_token"`  // Twilio or Vonage secret
	APIKey      string `mapstructure:"api_key"`     // Vonage
	BaseURL     string `mapstructure:"base_url"`    // override for testing / regional endpoints
	ReplyHeader string `mapstructure:"reply_header"`
}

// IMessageConfig configures the BlueBubbles-backed iMessage transport.
type IMessageConfig struct {
	BaseURL      string `mapstructure:"base_url"`      // "http://localhost:1234"
	Password     string `mapstructure:"password"`      // BlueBubbles server password
	ChatGUID     string `mapstructure:"chat_guid"`     // outbound target
	PollInterval string `mapstructure:"poll_interval"` // duration string, e.g. "2s"
	ReplyHeader  string `mapstructure:"reply_header"`
	// Allowlist holds iMessage handles (phone numbers or Apple ID
	// addresses, as BlueBubbles reports them) allowed to reach the agent.
	Allowlist []string `mapstructure:"allowlist"`
	// AllowGroups lets the agent answer in group chats, channels and
	// rooms. Off by default: every member reads the reply. When on,
	// each (sender, conversation) pair gets its own session.
	AllowGroups bool `mapstructure:"allow_groups"`
}

// EmailConfig configures the IMAP+SMTP email transport.
type EmailConfig struct {
	IMAPAddr     string `mapstructure:"imap_addr"`
	IMAPUsername string `mapstructure:"imap_username"`
	IMAPPassword string `mapstructure:"imap_password"`
	Mailbox      string `mapstructure:"mailbox"`
	PollInterval string `mapstructure:"poll_interval"`

	SMTPAddr     string `mapstructure:"smtp_addr"`
	SMTPUsername string `mapstructure:"smtp_username"`
	SMTPPassword string `mapstructure:"smtp_password"`

	From        string `mapstructure:"from"`
	ReplyHeader string `mapstructure:"reply_header"`
	// Allowlist holds sender addresses allowed to reach the agent,
	// matched case-insensitively. A From header is trivially forged;
	// set RequireAuthenticationResults so the allow-list is only
	// consulted for mail your inbound MTA authenticated.
	Allowlist []string `mapstructure:"allowlist"`
	// RequireAuthenticationResults drops any message whose
	// Authentication-Results header (added by the receiving MTA)
	// does not carry dkim=pass for the From domain. Off by default
	// because not every MTA adds the header; the daemon logs a
	// warning at startup when it is off.
	RequireAuthenticationResults bool `mapstructure:"require_authentication_results"`
	// TrustedAuthservID is the authserv-id your receiving MTA writes
	// in Authentication-Results (e.g. "mx.example.com"). Required with
	// require_authentication_results: headers carrying any other id
	// are ignored, because senders can write them.
	TrustedAuthservID string `mapstructure:"trusted_authserv_id"`
	// InsecureTrustFrom lets an allowlist key on the unauthenticated
	// From header. Only for test inboxes: anyone can then act as any
	// allowlisted sender.
	InsecureTrustFrom bool `mapstructure:"insecure_trust_from"`
}

// SlackConfig configures the Slack Socket Mode transport.
type SlackConfig struct {
	AppToken    string   `mapstructure:"app_token"`
	BotToken    string   `mapstructure:"bot_token"`
	BotUserID   string   `mapstructure:"bot_user_id"`
	ReplyHeader string   `mapstructure:"reply_header"`
	Allowlist   []string `mapstructure:"allowlist"`
	// AllowGroups lets the agent answer in group chats, channels and
	// rooms. Off by default: every member reads the reply. When on,
	// each (sender, conversation) pair gets its own session.
	AllowGroups bool `mapstructure:"allow_groups"`
}

// DiscordConfig configures the Discord Gateway transport.
type DiscordConfig struct {
	Token       string   `mapstructure:"token"`
	ReplyHeader string   `mapstructure:"reply_header"`
	Allowlist   []string `mapstructure:"allowlist"`
	// AllowGroups lets the agent answer in group chats, channels and
	// rooms. Off by default: every member reads the reply. When on,
	// each (sender, conversation) pair gets its own session.
	AllowGroups bool `mapstructure:"allow_groups"`
}

// MatrixConfig configures the Matrix client-server transport.
type MatrixConfig struct {
	HomeserverURL string   `mapstructure:"homeserver_url"`
	AccessToken   string   `mapstructure:"access_token"`
	UserID        string   `mapstructure:"user_id"`
	ReplyHeader   string   `mapstructure:"reply_header"`
	Allowlist     []string `mapstructure:"allowlist"`
	// AllowGroups lets the agent answer in group chats, channels and
	// rooms. Off by default: every member reads the reply. When on,
	// each (sender, conversation) pair gets its own session.
	AllowGroups bool `mapstructure:"allow_groups"`
}

// TelegramConfig configures the Telegram Bot API transport.
type TelegramConfig struct {
	Token       string   `mapstructure:"token"`
	BaseURL     string   `mapstructure:"base_url"`
	ReplyHeader string   `mapstructure:"reply_header"`
	Allowlist   []string `mapstructure:"allowlist"`
	// AllowGroups lets the agent answer in group chats, channels and
	// rooms. Off by default: every member reads the reply. When on,
	// each (sender, conversation) pair gets its own session.
	AllowGroups bool `mapstructure:"allow_groups"`
}

// SignalConfig configures the signal-cli transport.
type SignalConfig struct {
	// Binary is the signal-cli executable. Empty defaults to "signal-cli".
	Binary string `mapstructure:"binary"`
	// Account is the E.164 phone number the daemon runs as.
	Account string `mapstructure:"account"`
	// ExtraArgs are inserted between `-a <account>` and `jsonRpc`.
	ExtraArgs []string `mapstructure:"extra_args"`
	// ReplyHeader is prepended to every outbound message.
	ReplyHeader string `mapstructure:"reply_header"`
	// Allowlist restricts inbound handling to these E.164 numbers.
	Allowlist []string `mapstructure:"allowlist"`
	// AttachmentsDir is the local path where signal-cli persists
	// received attachments (typically `<signal-cli-data>/attachments`).
	// Required when `media.audio.backend` is configured and the
	// operator wants voice notes routed through transcription.
	AttachmentsDir string `mapstructure:"attachments_dir"`
	// AllowGroups lets the agent answer in group chats, channels and
	// rooms. Off by default: every member reads the reply. When on,
	// each (sender, conversation) pair gets its own session.
	AllowGroups bool `mapstructure:"allow_groups"`
}

// WhatsAppConfig groups the whatsapp transport tuning knobs.
type WhatsAppConfig struct {
	// ReplyHeader is prepended to every outbound message. Empty uses
	// the built-in default ("💎 *Rousseau Agent*\n\n"). Set to a single
	// space " " to disable the prefix entirely.
	ReplyHeader string `mapstructure:"reply_header"`
	// Voice enables whisper-based transcription for inbound voice
	// notes. When disabled, audio messages are logged and skipped.
	Voice VoiceConfig `mapstructure:"voice"`
}

// VoiceConfig configures voice-note transcription.
type VoiceConfig struct {
	// Enabled toggles the whisper transcriber. Off by default because
	// it requires the whisper.cpp CLI to be installed.
	Enabled bool `mapstructure:"enabled"`
	// Binary is the whisper CLI to invoke. Empty defaults to "whisper".
	Binary string `mapstructure:"binary"`
	// Model is passed to --model (e.g. "base.en", "small").
	Model string `mapstructure:"model"`
	// ModelPath is an explicit .bin path; takes precedence over Model.
	ModelPath string `mapstructure:"model_path"`
	// Language is passed to --language. Empty auto-detects.
	Language string `mapstructure:"language"`
	// ExtraArgs are appended to every whisper invocation.
	ExtraArgs []string `mapstructure:"extra_args"`
}
