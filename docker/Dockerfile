# syntax=docker/dockerfile:1.7
#
# Multi-stage build for the rousseau-agent WhatsApp daemon.
#
#  builder    Go toolchain, compiles a static rousseau binary.
#  runtime    node:alpine base (needed for the claude CLI), non-root
#             rousseau user, static Go binary + npm-installed claude.
#
# The final image installs no shell tooling beyond what claude and
# rousseau need. The image is not pinned to a digest here because we
# rebuild locally; add `--platform` and `--pull=always` to your build
# command if you want a hermetic pin.

########################  BUILDER  ########################
ARG GO_VERSION=1.26
FROM golang:${GO_VERSION}-alpine AS builder

WORKDIR /src

# Cache modules independently of source.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .

# Static binary. CGO_ENABLED=0 makes modernc/sqlite the sqlite driver
# and avoids libc coupling to the runtime image.
ARG VERSION=container
ARG COMMIT=unknown
ARG BUILD_DATE=unknown
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    CGO_ENABLED=0 GOOS=linux \
    go build \
        -trimpath \
        -ldflags="-s -w \
          -X 'github.com/sebastienrousseau/rousseau-agent/internal/cli.version=${VERSION}' \
          -X 'github.com/sebastienrousseau/rousseau-agent/internal/cli.commit=${COMMIT}' \
          -X 'github.com/sebastienrousseau/rousseau-agent/internal/cli.buildDate=${BUILD_DATE}'" \
        -o /out/rousseau ./cmd/rousseau

########################  RUNTIME  ########################
FROM node:22-alpine AS runtime

# Install the claude CLI. Pinned to a known-good major; bump when we
# validate a newer one.
ARG CLAUDE_VERSION=latest
RUN npm install -g @anthropic-ai/claude-code@${CLAUDE_VERSION} \
 && node --version && claude --version

# Non-root user. UID/GID 1000 lines up with the typical single-user
# host so bind-mounts inherit ownership without extra chowning in
# rootless podman. The node:alpine base already ships a user at UID
# 1000; drop it first, then create rousseau at the same id.
RUN deluser --remove-home node 2>/dev/null || true \
 && delgroup node        2>/dev/null || true \
 && addgroup -g 1000 rousseau \
 && adduser  -u 1000 -G rousseau -h /home/rousseau -s /bin/sh -D rousseau

# Copy the static Go binary from the builder.
COPY --from=builder /out/rousseau /usr/local/bin/rousseau

# Bind-mount targets: state (RW) and claude auth (RW so the CLI can
# refresh cached tokens). Workspace mounts under /workspace.
RUN mkdir -p /home/rousseau/.local/share/rousseau \
              /home/rousseau/.claude \
              /workspace \
 && chown -R rousseau:rousseau /home/rousseau /workspace

USER rousseau
WORKDIR /workspace
ENV HOME=/home/rousseau

# rousseau has no HTTP surface — no ports to expose. Health is the
# process being alive; podman restart policy handles crashes.
ENTRYPOINT ["/usr/local/bin/rousseau"]
CMD ["whatsapp", "--allow", "447906009073@s.whatsapp.net"]
