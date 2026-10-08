package claudecli

import (
	"context"
	"os"
	"os/exec"

	"github.com/sebastienrousseau/rousseau-agent/internal/envscrub"
)

// defaultEnv is what claude needs beyond envscrub.DefaultAllow (which
// keeps HOME, so ~/.claude credentials still resolve): its own API
// settings, and proxy and CA settings for networks that intercept TLS.
// Everything else the daemon holds (channel tokens, other providers'
// keys, database DSNs) stays out of the child, which runs its own Bash
// tool and would otherwise hand them to anything that can prompt it.
var defaultEnv = []string{
	"ANTHROPIC_*", "CLAUDE_*",
	"HTTPS_PROXY", "HTTP_PROXY", "NO_PROXY", "https_proxy", "http_proxy", "no_proxy",
	"NODE_EXTRA_CA_CERTS", "SSL_CERT_FILE", "SSL_CERT_DIR",
}

// command builds every claude invocation, so no call site can forget
// the scrubbed environment.
func (p *Provider) command(ctx context.Context, args []string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, p.cfg.Binary, args...)
	cmd.Env = p.env()
	return cmd
}

// env is the child environment: envscrub's baseline, defaultEnv, and
// the operator's EnvPassthrough.
func (p *Provider) env() []string {
	allow := make([]string, 0, len(defaultEnv)+len(p.cfg.EnvPassthrough))
	allow = append(allow, defaultEnv...)
	allow = append(allow, p.cfg.EnvPassthrough...)
	return envscrub.Scrub(os.Environ(), allow)
}
