# rousseau-agent — container tags + arch matrix

rousseau-agent ships in three container flavours (`:latest`,
`:distroless`, `:lite`) and five Linux architectures
(`amd64`, `arm64`, `armv6`, `armv7`, `riscv64`), plus macOS + Windows
for the CLI archive.

Verified binary sizes (release-flags `-s -w -trimpath`):

| Arch | rousseau | rousseau-lite |
| :--- | :---: | :---: |
| linux/amd64   | 50.6 MB | 43.3 MB |
| linux/arm64   | 47.7 MB | 40.3 MB |
| linux/armv6   | 47.2 MB | 40.0 MB |
| linux/armv7   | 47.2 MB | 40.0 MB |
| linux/riscv64 | 46.9 MB | 39.8 MB |

Every triple is compile-verified on every push by
`.github/workflows/cross-arch.yml` so a Raspberry Pi Zero (armv6)
or a SiFive HiFive (riscv64) operator is never a release-cycle
regression away from an unbuildable binary.

Pick the container flavour that matches your deployment style; the
runtime behaviour is identical across all three.

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

## `ghcr.io/sebastienrousseau/rousseau-agent:lite`

**Base:** `gcr.io/distroless/static-debian12:nonroot`
**Size:** ~46 MB compressed (~14% smaller than `:distroless`)
**Excludes:** the whatsmeow-backed WhatsApp transport (compiled out
behind `//go:build no_whatsmeow`); every other transport (signal,
telegram, matrix, slack, discord, sms, imessage, email) is included.
**Pick this when:** you don't need WhatsApp. All CLI-visible surface
survives — `rousseau whatsapp` still exists, but its `Start`,
`Deliver`, and `Transcribe` methods return an "unavailable" error at
runtime rather than silently doing nothing. That way, an operator
who enables the whatsapp transport in a `:lite` build sees exactly
why it doesn't work instead of debugging a silent no-op.

## How to build locally

```bash
# :latest (default)
podman build -f docker/Dockerfile -t rousseau-agent:latest .

# :distroless
podman build -f docker/Dockerfile.distroless -t rousseau-agent:distroless .

# :lite (no whatsmeow)
podman build -f docker/Dockerfile.lite -t rousseau-agent:lite .
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
