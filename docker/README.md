# rousseau-agent — container tags

rousseau-agent ships in three flavours. Pick the one that matches your
deployment style; the runtime behaviour is identical across all three.

## `ghcr.io/sebastienrousseau/rousseau-agent:latest`

**Base:** `node:22-alpine` (Alpine + Node.js runtime + `@anthropic-ai/claude-code`)
**Size:** ~530 MB compressed
**Includes:** the rousseau binary **plus** the `claude` CLI, so you
can run `rousseau chat` using the CLI-backed provider out of the box.
**Pick this when:** you want the "unbox, `podman run`, done" story on
a machine that isn't already running claude-code.

## `ghcr.io/sebastienrousseau/rousseau-agent:distroless`

**Base:** `gcr.io/distroless/static-debian12:nonroot`
**Size:** ~55 MB compressed (~20× smaller than `:latest`)
**Includes:** only the rousseau binary. TLS root certs are baked in
via the distroless base.
**Excludes:** `claude` CLI. Use a direct provider (`anthropic`,
`bedrock`, `vertex`, `openai`, `openrouter`, `ollama`) or bind-mount
`claude` from the host at `/usr/local/bin/claude`.
**Pick this when:** you already run `claude` on the host, use a direct
provider, or need the smaller footprint for edge / mobile-server /
constrained-flash deployments.

## `ghcr.io/sebastienrousseau/rousseau-agent:lite` *(planned)*

**Base:** distroless static
**Size target:** ~25 MB compressed
**Excludes:** the whatsmeow-backed WhatsApp transport (compiled out
behind a `no_whatsmeow` build tag).
**Pick this when:** you don't need WhatsApp and want the smallest
possible footprint. Ships in a follow-up commit once the build-tag
surgery in `internal/transport/whatsapp/` lands (§3 of
`docs/IMPLEMENTATION_PLAN_2026_07_16.md`).

## How to build locally

```bash
# :latest (default)
podman build -f docker/Dockerfile -t rousseau-agent:latest .

# :distroless
podman build -f docker/Dockerfile.distroless -t rousseau-agent:distroless .
```

Verify the resulting binary is reproducible:

```bash
SOURCE_DATE_EPOCH=$(git log -1 --pretty=%ct) \
GOFLAGS="-trimpath -buildvcs=false" \
GOTOOLCHAIN=local \
go build -ldflags="-s -w -buildid=" -o /tmp/rousseau-a ./cmd/rousseau
sha256sum /tmp/rousseau-a
```

Re-run the same command; the sha256 must be identical.

## How to pick between distroless and latest at deploy time

| Signal | `:latest` | `:distroless` |
| :-- | :-: | :-: |
| Need `claude` CLI on the host | ❌ | ✅ (bind-mount or install separately) |
| Constrained flash / edge device | ❌ | ✅ |
| Kubernetes distroless-preferred policy | ❌ | ✅ |
| Home-lab NAS with 10 GB free | ✅ | ✅ |
| Ephemeral dev container | ✅ | 🟡 (works but needs claude bind-mount) |

## Rootless podman + Quadlet

Both tags run under rootless podman + systemd Quadlet. Copy
`rousseau-agent.container` from this directory to
`$XDG_CONFIG_HOME/containers/systemd/`, edit the image tag if you want
`:distroless`, then:

```bash
systemctl --user daemon-reload
systemctl --user start rousseau-agent
```

## Egress allowlist

Neither container needs inbound network. Outbound calls only reach
the LLM provider you configured plus the messaging transport
endpoints (`s.whatsapp.net`, Slack, Discord, etc.). Sample nftables
egress-allowlist ships in `docker/nftables.example.conf`.

## Signature verification

Every image tag published to `ghcr.io/sebastienrousseau/rousseau-agent`
is signed with cosign under GitHub OIDC. Verify before pulling:

```bash
cosign verify \
  --certificate-identity-regexp 'https://github\.com/sebastienrousseau/rousseau-agent/\.github/workflows/.+' \
  --certificate-oidc-issuer 'https://token.actions.githubusercontent.com' \
  ghcr.io/sebastienrousseau/rousseau-agent:distroless
```
