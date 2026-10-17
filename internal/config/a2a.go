package config

// A2AConfig configures the Agent-to-Agent protocol surface — both the
// server (rousseau accepts inbound tasks from peer agents) and the
// client peer list (rousseau dispatches to configured peers). Empty
// leaves A2A off; see docs/a2a.md for the wire protocol.
type A2AConfig struct {
	// Server configures the inbound A2A HTTP endpoint. Empty
	// (Enabled=false) leaves the server off.
	Server A2AServerConfig `mapstructure:"server"`
	// Clients lists per-peer client configurations. Each entry
	// creates a client.Client the daemon can dispatch to via the
	// (not-yet-wired) agent-side A2A tool.
	Clients []A2AClientConfig `mapstructure:"clients"`
}

// A2AServerConfig is the inbound-side surface.
type A2AServerConfig struct {
	// Enabled turns the A2A server on. Off by default so the daemon
	// doesn't expose a network surface without operator opt-in.
	Enabled bool `mapstructure:"enabled"`
	// Listen is the bind address (Go net.Listen syntax). Empty
	// defaults to 127.0.0.1:8443 (loopback only). A non-loopback bind
	// requires TLSCertFile + TLSKeyFile, or AllowPlaintext; otherwise
	// the A2A server refuses to start.
	Listen string `mapstructure:"listen"`
	// TLSCertFile and TLSKeyFile are the PEM certificate chain and
	// private key the A2A server terminates TLS with. Set both or
	// neither.
	TLSCertFile string `mapstructure:"tls_cert_file"`
	TLSKeyFile  string `mapstructure:"tls_key_file"`
	// AllowPlaintext permits a non-loopback Listen without TLS, for a
	// deployment where a reverse proxy on a private network terminates
	// TLS. Bearer tokens and task content cross that hop in clear text.
	AllowPlaintext bool `mapstructure:"allow_plaintext"`
	// MaxInflightPerPeer caps running tasks per authenticated peer.
	// Over the cap a new task is refused with 429 (JSON-RPC -32029).
	// Zero uses 4.
	MaxInflightPerPeer int `mapstructure:"max_inflight_per_peer"`
	// MaxInflight caps running tasks across all peers. Zero uses 32.
	MaxInflight int `mapstructure:"max_inflight"`
	// AuthTokensFile points at a newline-separated bearer-token
	// allowlist. Every non-empty non-comment line is one accepted
	// token. Comments start with '#'. Empty disables auth — DO NOT
	// deploy without setting this.
	AuthTokensFile string `mapstructure:"auth_tokens_file"`
	// SigningKeyFile is the base64-encoded Ed25519 private key file
	// (see `rousseau a2a keygen`). When set, the well-known
	// AgentCard route JWS-signs its response so peers with the
	// corresponding public key can verify authenticity.
	SigningKeyFile string `mapstructure:"signing_key_file"`
	// AgentName is what appears on the served CapabilityCard's Name
	// field. Empty defaults to "rousseau-agent".
	AgentName string `mapstructure:"agent_name"`
	// AgentID is the stable identifier peers use to refer to this
	// deployment. Empty defaults to the operator's hostname.
	AgentID string `mapstructure:"agent_id"`
	// ExposedSkills is the allow-list of skill names peers can
	// invoke via the SkillName field on a Task. Empty means no
	// skill is exposed — the default handler receives every task.
	ExposedSkills []string `mapstructure:"exposed_skills"`
	// PublicURL is the externally reachable base URL peers use (e.g.
	// https://agent.example.com). The well-known AgentCard advertises
	// it and is JWS-signed only when it is set together with
	// SigningKeyFile. Empty serves a card whose URL comes from the
	// request, unsigned, so a forged Host / X-Forwarded-Host can never
	// obtain a card signed by the operator's key.
	PublicURL string `mapstructure:"public_url"`
	// TrustedProxies lists reverse-proxy addresses (IP or CIDR) whose
	// X-Forwarded-Proto / X-Forwarded-Host headers are honoured when
	// deriving the card URL. Empty ignores those headers from every
	// client. An unparsable entry refuses to start the A2A server.
	TrustedProxies []string `mapstructure:"trusted_proxies"`
}

// A2AClientConfig is one outbound-peer entry.
type A2AClientConfig struct {
	// Name identifies the peer in logs, metrics, and (future)
	// tool-call routing. Required.
	Name string `mapstructure:"name"`
	// Endpoint is the peer's A2A base URL (e.g.
	// https://spec-writer.internal). Required.
	Endpoint string `mapstructure:"endpoint"`
	// AuthHeaderEnv names the env var whose value goes on the
	// Authorization header verbatim (typically "Bearer <token>").
	// Empty disables auth for this peer.
	AuthHeaderEnv string `mapstructure:"auth_header_env"`
	// TrustedPublisherKeys are file paths to base64-encoded Ed25519
	// public keys the operator has authorised to sign this peer's
	// AgentCard. When non-empty, the client verifies the card's
	// signatures[] before treating it as authoritative, and refuses
	// to fall back to the peer's legacy (unsignable) card.
	TrustedPublisherKeys []string `mapstructure:"trusted_publisher_keys"`
	// RequireSignedCard, when true, causes the client to reject
	// unsigned cards even in the "no trusted key list" case. Use
	// against peers you've onboarded via out-of-band key exchange.
	RequireSignedCard bool `mapstructure:"require_signed_card"`
}
