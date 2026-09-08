package cli

// Daemon-side A2A server assembly. Called from assembleDaemon;
// registered with StartBackgroundServers. Fail-open: any
// configuration error at boot logs at WARN and returns nil so the
// daemon still runs its chat transports — an A2A misconfig must not
// take the deployment offline.

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/sebastienrousseau/rousseau-agent/internal/a2a"
	"github.com/sebastienrousseau/rousseau-agent/internal/a2a/server"
	"github.com/sebastienrousseau/rousseau-agent/internal/agent"
	"github.com/sebastienrousseau/rousseau-agent/internal/config"
)

// defaultA2AListen is the shipped bind address. Non-privileged port
// per the deployment story — 8443 needs no capabilities.
const defaultA2AListen = ":8443"

// defaultA2AAgentName is what shows up on the served CapabilityCard
// when the operator hasn't set agent_name.
const defaultA2AAgentName = "rousseau-agent"

// a2aRuntime holds the assembled A2A server + its bind address, in
// the same shape as scimServer/scimAddr on the daemon wiring. Nil
// when A2A is not configured or fails to assemble.
type a2aRuntime struct {
	Server *server.Server
	Addr   string
	// AuthCount is the number of bearer tokens loaded (0 when auth
	// was disabled by the operator). Surfaced through doctor rows so
	// operators can catch the "I forgot to populate the token file"
	// misconfig at diagnostic time, not at first peer request.
	AuthCount int
	// Signed is true when a signing key was successfully loaded so
	// the served AgentCard carries a JWS signature.
	Signed bool
}

// buildA2AServer composes an [a2a/server.Server] from cfg. Returns
// nil on any disable-shaped condition — cfg.Enabled=false, missing
// files logged as WARN, etc.
//
// Design mirrors buildSCIM's three-condition pattern:
//   - Not configured  → return nil, no log.
//   - Configured but broken → WARN, return nil (daemon keeps
//     running the chat transports).
//   - Configured + healthy → return server + addr.
func buildA2AServer(cfg config.A2AConfig, ag *agent.Agent, logger *slog.Logger) *a2aRuntime {
	if !cfg.Server.Enabled {
		return nil
	}
	if ag == nil {
		logger.Warn("a2a.no_agent",
			slog.String("hint", "A2A server needs a running agent to dispatch tasks to; nothing wired"),
		)
		return nil
	}

	listen := cfg.Server.Listen
	if listen == "" {
		listen = defaultA2AListen
	}
	agentName := cfg.Server.AgentName
	if agentName == "" {
		agentName = defaultA2AAgentName
	}
	agentID := cfg.Server.AgentID
	if agentID == "" {
		if host, err := os.Hostname(); err == nil {
			agentID = host
		} else {
			agentID = agentName
		}
	}

	tokens, err := loadA2ABearerTokens(cfg.Server.AuthTokensFile, logger)
	if err != nil {
		logger.Warn("a2a.auth_tokens_unreadable",
			slog.String("file", cfg.Server.AuthTokensFile),
			slog.String("err", err.Error()),
		)
		return nil
	}
	if len(tokens) == 0 && cfg.Server.AuthTokensFile != "" {
		logger.Warn("a2a.auth_tokens_empty",
			slog.String("file", cfg.Server.AuthTokensFile),
			slog.String("hint", "the file contains no non-comment lines; A2A would run without auth"),
		)
		return nil
	}
	if len(tokens) == 0 {
		logger.Warn("a2a.no_auth",
			slog.String("hint", "auth_tokens_file is empty — A2A server would accept unauthenticated calls; refusing to boot"),
		)
		return nil
	}

	skills := make([]a2a.SkillDescriptor, 0, len(cfg.Server.ExposedSkills))
	for _, name := range cfg.Server.ExposedSkills {
		skills = append(skills, a2a.SkillDescriptor{Name: name})
	}
	card := a2a.CapabilityCard{
		AgentID:           agentID,
		Name:              agentName,
		Version:           Version(),
		Skills:            skills,
		SupportsStreaming: true,
		PublishedAt:       time.Now().UTC(),
	}

	handler := newA2ATaskHandler(ag, cfg.Server.ExposedSkills, logger)
	srv, err := server.New(card, handler, tokens)
	if err != nil {
		logger.Warn("a2a.server_new", slog.String("err", err.Error()))
		return nil
	}

	rt := &a2aRuntime{Server: srv, Addr: listen, AuthCount: len(tokens)}
	if cfg.Server.SigningKeyFile != "" {
		key, err := loadA2AServerSigningKey(cfg.Server.SigningKeyFile)
		if err != nil {
			logger.Warn("a2a.signing_key_unreadable",
				slog.String("file", cfg.Server.SigningKeyFile),
				slog.String("err", err.Error()),
				slog.String("hint", "AgentCard will serve unsigned; peers with RequireSignedCard will reject"),
			)
		} else {
			srv.SigningKey = key
			rt.Signed = true
		}
	}
	logger.Info("a2a.server_configured",
		slog.String("addr", listen),
		slog.String("agent_id", agentID),
		slog.Int("bearer_tokens", len(tokens)),
		slog.Int("exposed_skills", len(cfg.Server.ExposedSkills)),
		slog.Bool("signed_card", rt.Signed),
	)
	return rt
}

// loadA2ABearerTokens reads the newline-separated bearer-token
// allowlist. Blank lines and lines starting with '#' are ignored so
// operators can annotate their token files. Returns an empty slice
// (not an error) when the file is missing — operator opts out by
// leaving the file empty.
func loadA2ABearerTokens(path string, _ *slog.Logger) ([]string, error) {
	if path == "" {
		return nil, nil
	}
	body, err := os.ReadFile(path) //nolint:gosec // operator-supplied path
	if err != nil {
		return nil, err
	}
	var tokens []string
	for _, line := range strings.Split(string(body), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		tokens = append(tokens, trimmed)
	}
	return tokens, nil
}

// loadA2AServerSigningKey reads the base64-encoded Ed25519 private
// key. Shared surface with `rousseau a2a keygen` so operators can
// use one keyring for both publisher + server signing.
func loadA2AServerSigningKey(path string) (ed25519.PrivateKey, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // operator-supplied path
	if err != nil {
		return nil, err
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	if len(decoded) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("key %s is %d bytes, want %d", path, len(decoded), ed25519.PrivateKeySize)
	}
	return ed25519.PrivateKey(decoded), nil
}

// runA2AServer runs the assembled server against the configured
// address until ctx cancels. Called from StartBackgroundServers in
// its own goroutine. A bind failure is a critical operator misconfig
// — logged at ERROR but doesn't abort the daemon (other transports
// keep running).
func runA2AServer(ctx context.Context, rt *a2aRuntime, logger *slog.Logger) {
	if rt == nil {
		return
	}
	ln, err := net.Listen("tcp", rt.Addr)
	if err != nil {
		logger.Error("a2a.listen_failed",
			slog.String("addr", rt.Addr),
			slog.String("err", err.Error()),
		)
		return
	}
	logger.Info("a2a.listening", slog.String("addr", ln.Addr().String()))

	httpSrv := &http.Server{
		Handler:           rt.Server.Router(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	done := make(chan error, 1)
	go func() { done <- httpSrv.Serve(ln) }()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutdownCtx) //nolint:errcheck // best-effort during shutdown
		<-done
	case err := <-done:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Warn("a2a.serve_ended", slog.String("err", err.Error()))
		}
	}
}
