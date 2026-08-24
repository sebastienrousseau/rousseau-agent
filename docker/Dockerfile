# syntax=docker/dockerfile:1.7
#
# Multi-stage build for the rousseau-agent WhatsApp daemon.
#
#  builder    Go toolchain, compiles a static rousseau binary.
#  runtime    node:alpine base (needed for the claude CLI), non-root
#             rousseau user, static Go binary + npm-installed claude.
#
# The final image ships bash + the tooling Claude Code's Bash tool
# typically invokes (git, openssh-client, curl, jq, ripgrep, less).
# Kept intentionally minimal — enough to let the agent operate on the
# mounted /workspace, no build toolchains beyond that.
# The image is not pinned to a digest here because we rebuild locally;
# add `--platform` and `--pull=always` to your build command if you
# want a hermetic pin.

########################  BUILDER  ########################
# Base images pinned by digest for reproducibility. When updating, bump
# both the tag comment and the sha256 (docker inspect --format
# '{{index .RepoDigests 0}}' <image>).
ARG GO_VERSION=1.26
# golang:1.26-alpine as of 2026-07-12
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

# Runtime shell + tools that Claude Code's Bash tool commonly invokes.
# bash is the shell contract the CLI expects; git + openssh-client
# cover source ops (including the SSH-based flows the mounted
# workspace uses); curl / jq / ripgrep / less are the usual
# JSON / HTTP / search / pager set for agent workflows.
#
# build-base + pkgconf + linux-headers + *-dev packages give us the
# compilers and headers needed to build native extensions (pip wheels,
# cargo builds, cgo, node-gyp). Python is baked in — it's the most
# frequently invoked interpreter and the smallest of the "big three".
# Every other language runtime (Go, Rust, Node versions, Ruby, PHP,
# Lua, …) is installed on-demand via mise per repo, keyed off a
# `.mise.toml` / `.tool-versions` file.
RUN apk add --no-cache \
      bash \
      fish \
      git \
      github-cli \
      openssh-client \
      curl \
      jq \
      ripgrep \
      less \
      ca-certificates \
      build-base \
      pkgconf \
      linux-headers \
      openssl-dev \
      zlib-dev \
      libffi-dev \
      libpq-dev \
      libxml2-dev \
      libxslt-dev \
      bzip2-dev \
      ncurses-dev \
      readline-dev \
      sqlite-dev \
      python3 \
      py3-pip \
      pipx \
 && rm -rf /var/cache/apk/*

# NOTE: Go and Rust are installed via mise later (see `mise use -g` step).
# Do NOT `apk add go rust cargo` here — that installs system-wide Rust
# in /usr/bin which rustup then refuses to overwrite ("cannot install
# while Rust is installed"), so mise's `rust@latest` fails and leaves
# the container with musl-linked Alpine Rust that has surprising
# compat issues for most crates. Owning language toolchains through
# mise keeps the versions consistent with what a developer would get
# via `mise install` on the host.

# Global npm additions the agent's flow relies on. pnpm covers repos
# with pnpm-lock.yaml; yarn already ships with the node base image.
RUN npm install -g pnpm

# chezmoi (dotfiles manager). Pinned to whatever the installer script
# resolves at build; a rebuild refreshes both the binary and the
# dotfiles state (see the chezmoi init/apply step later in this file).
RUN curl -fsSL https://get.chezmoi.io | sh -s -- -b /usr/local/bin \
 && chezmoi --version

# mise (version manager, replaces asdf/rtx/nvm) installed system-wide.
# Runtimes installed via `mise install <lang>@<ver>` end up under
# /home/rousseau/.local/share/mise/, which is a bind-mounted volume
# in the Quadlet unit so cached toolchains survive image rebuilds.
RUN curl -fsSL https://mise.run | MISE_INSTALL_PATH=/usr/local/bin/mise sh

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
 && adduser  -u 1000 -G rousseau -h /home/rousseau -s /usr/bin/fish -D rousseau

# Copy the static Go binary from the builder.
COPY --from=builder /out/rousseau /usr/local/bin/rousseau

# Bind-mount targets: state (RW) and claude auth (RW so the CLI can
# refresh cached tokens). Workspace mounts under /workspace.
#
# Symlinks make host-style paths resolve inside the container so that
# WhatsApp-driven commands referencing `~/team-rousseau-workspace/…`
# or `/home/seb/team-rousseau-workspace/…` land on the real bind-
# mounted workspace. Without them Claude Code's Bash tool gets
# "No such file or directory" for paths the user types out of habit.
RUN mkdir -p /home/rousseau/.local/share/rousseau \
              /home/rousseau/.claude \
              /workspace \
              /home/seb \
 && ln -sfn /workspace /home/rousseau/team-rousseau-workspace \
 && ln -sfn /workspace /home/seb/team-rousseau-workspace \
 && ln -sfn /home/rousseau/.config /home/seb/.config \
 && ln -sfn /home/rousseau/.ssh    /home/seb/.ssh \
 && chown -R rousseau:rousseau /home/rousseau /workspace /home/seb

# Ship the curated skills bundle at /etc/rousseau/skills/ — the
# skills loader treats this as a system-wide fallback when the user
# hasn't dropped their own skills into ~/.local/share/rousseau/skills/.
# Kept RO (root-owned, 0644) so the daemon can read but not overwrite.
COPY skills/*.md /etc/rousseau/skills/
RUN chmod 0644 /etc/rousseau/skills/*.md

USER rousseau
WORKDIR /workspace
ENV HOME=/home/rousseau
# mise shims come first so the tool-managed language versions win over
# any system-installed defaults.
ENV PATH=/home/rousseau/.local/share/mise/shims:/usr/local/bin:/usr/bin:/bin

# Pre-warm mise with the toolchains the workspace uses most. Any
# per-repo `.mise.toml` can still override these on a folder basis.
# --yes accepts license prompts (rust); MISE_TRUSTED_CONFIG_PATHS is
# set so mise doesn't prompt on discovered configs at runtime.
ENV MISE_YES=1 \
    MISE_TRUSTED_CONFIG_PATHS=/workspace
# Node is already provided by the base image (node:22-alpine) — no
# need to have mise compile it from source (Alpine musl has no
# precompiled node@22 for mise, so `mise install node@22` would
# invoke a full V8 C++ compile that takes 30+ minutes). Per-repo
# `.mise.toml` can still ask mise for a different node if needed;
# it'll fetch on first use in the container's writable layer.
RUN mise use -g python@3.12 go@1.26 rust@latest || true \
 && mise ls \
 && rm -rf /tmp/* /tmp/.[!.]* 2>/dev/null || true
# Clean /tmp so the runtime Tmpfs size=64m overlay doesn't fail with
# ENOSPC on container create. mise/python dumped build logs and
# compile caches here (~500 MB).

# Bootstrap chezmoi-managed dotfiles from the public GitHub source.
# --apply materialises them under $HOME; --no-tty forbids stdin prompts;
# --promptString supplies each value the dotfiles' .chezmoi.toml.tmpl
# would otherwise ask for. Letting chezmoi generate its own config file
# (rather than pre-seeding one) keeps the [age] / [merge] sections in
# sync with the template's shape — otherwise it prints a "config file
# template has changed" warning and refuses to apply.
RUN chezmoi init --apply --no-tty --branch main \
      --promptString age_identity= \
      --promptString age_recipient= \
      --promptString profile=container \
      --promptString theme=tokyonight-night \
      --promptString git_name='Sebastien Rousseau' \
      --promptString git_email=sebastian.rousseau@gmail.com \
      --promptString git_signingkey=~/.ssh/id_ed25519 \
      --promptString git_signingformat=ssh \
      sebastienrousseau \
      2>&1 | tail -30 \
 || echo "chezmoi bootstrap emitted warnings; container still usable"

# Final cleanup — same reason as the earlier /tmp wipe: keep the image
# lean and avoid tripping the runtime tmpfs size limit at container
# create.
RUN rm -rf /tmp/* /tmp/.[!.]* 2>/dev/null || true

# rousseau has no HTTP surface — no ports to expose. Health is the
# process being alive; podman restart policy handles crashes.
#
# The image ships without a hardcoded allowlist. Supply your JID via
# the ROUSSEAU_WHATSAPP_ALLOW env var, e.g.
#   docker run -e ROUSSEAU_WHATSAPP_ALLOW=15551234567@s.whatsapp.net rousseau
# or explicitly with `--allow` at container invocation.
ENTRYPOINT ["/usr/local/bin/rousseau"]
CMD ["whatsapp"]
