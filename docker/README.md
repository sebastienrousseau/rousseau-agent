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

* * *

## The build-environment images

Two images exist alongside the runtime tags above. They are not
deployment targets — they are the environment agents and CI use to
build the fleet.

| Image | Dockerfile | Purpose |
|---|---|---|
| `agent-base` | `docker/Dockerfile.base` | Ubuntu foundation shared by everything below |
| `agent-builder` | `docker/Dockerfile.builder` | Polyglot toolchain, sandbox prerequisites, supply-chain tooling |

They exist because the runtime daemon and a build environment want
opposite things. The daemon holds a WhatsApp session and Claude OAuth
credentials, so it runs `ReadOnly=true` with every capability dropped.
A build environment needs a writable filesystem and a large `/tmp`. So
the estate is split, and the invariant is:

> **No single container is both writable and credentialed.**

`agent-builder` carries no long-lived credentials. Scoped tokens are
injected per-run via `EnvironmentFile=` in its Quadlet. Do not add
credential mounts to it, and do not relax `ReadOnly=` on the daemon.

### Building

```bash
make images              # all five, podman by default
make image-builder       # base then builder, in order
make images ENGINE=docker
```

`ENGINE` defaults to `podman`. Plain builds work under Docker, but the
Quadlet units, `UserNS=keep-id` and the `pasta` network stack are
podman features, so `make quadlet-install` refuses to run under
anything else.

### Running under rootless podman

```bash
make container-check     # verify the host first
make quadlet-install     # copy units, daemon-reload
systemctl --user start rousseau-agent
systemctl --user start agent-builder
```

`make container-check` runs `docker/preflight.sh`, which checks the
things that otherwise fail at `systemctl --user start` with an opaque
message: a `/etc/subuid` and `/etc/subgid` range for your user,
unprivileged user namespaces enabled, a `pasta`/`passt` backend, a
reachable user systemd instance, the Quadlet generator (podman ≥ 4.4),
`/dev/fuse`, and the seccomp profile both units reference. It exits
non-zero with the count of failed checks, so it is usable as a CI gate.

### Why Ubuntu and not Alpine or CachyOS

glibc. musl breaks Python manylinux wheels, Node prebuilt native
modules, cgo-linked binaries and the official Swift toolchain — all of
which the repo fleet needs.

CachyOS was considered for host parity and rejected on two grounds.
Arch is a rolling release with no digest-equivalent pin, which would
regress the Pinned-Dependencies posture the rest of this repo
maintains; and [swift.org](https://www.swift.org/platform-support/)
ships official Linux toolchains for Ubuntu, Debian, Fedora, Amazon
Linux and RHEL UBI — not Arch. Host parity is bought instead through
chezmoi and mise version pins, which do not require a matching distro.

### Architecture

`agent-base` and `agent-builder` are **amd64 only**. The builder pins
release binaries that are published for amd64 alone (`yq_linux_amd64`,
`syft_..._linux_amd64`, `act_Linux_x86_64`). Publishing those under a
multi-arch manifest would produce a silently broken arm64 image, so
the manifest declares what it actually is. The runtime tags remain
multi-arch.
