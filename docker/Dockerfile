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

########################  BUILDER  ########################
# Base images are pinned by digest: a re-pushed tag cannot change what
# we build on. To update, pull the new tag and bump the digest
# (podman image inspect --format '{{index .RepoDigests 0}}' <image>).
# golang:1.26-alpine as of 2026-10-01
FROM golang:1.26-alpine@sha256:66f9a494af2b76ecb3eab75ff47166df61caec6d200c59d556b77327493d83a8 AS builder

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
# node:22-alpine as of 2026-10-01
FROM node:22-alpine@sha256:0a7108bf6c7bf5de370ffb1a3ed6be93d405b43ff159f681a8d18c0e2bc2e402 AS runtime

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
# `apk upgrade` first: the base image carries whatever CVEs Alpine had
# on its publication date, and this image is scanned on every build.
#
# fish was removed. bash is the shell contract Claude Code's Bash tool
# expects, so a second interactive shell added attack surface and a
# High-severity finding while serving nothing the daemon does.
RUN apk upgrade --no-cache \
 && apk add --no-cache \
      bash \
      git \
      openssh-client \
      gnupg \
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
# gnupg lets `git commit -S` verify signatures and the container's
# git flows call out to whatever key material the operator mounts
# (see the Quadlet drop-in for the .ssh / .gnupg bind mounts).

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
# npm itself is upgraded first: the copy bundled with the node base
# image carries vulnerable dependencies (brace-expansion, pacote,
# sigstore, ip-address, picomatch) that fail the image's CVE gate.
ARG NPM_VERSION=12.2.0
RUN npm install -g npm@${NPM_VERSION} && npm --version
RUN npm install -g pnpm

# chezmoi (dotfiles manager) and gh (GitHub CLI) are pinned release
# archives checked against SHA-256 sums recorded here. Alpine's
# packages lag upstream and were built with Go modules (x/crypto,
# x/net, grpc, go-git) that have published high-severity fixes. Bump a
# version together with both of its sums (from the release's
# checksums file, cross-checked against the downloaded archive).
ARG CHEZMOI_VERSION=2.73.0
# amd64 uses the musl archive (the plain amd64 build is glibc-linked
# and cannot run on Alpine); the arm64 archive is statically linked.
ARG CHEZMOI_SHA256_AMD64=09559524c5c724df9d452f1e78cfeca063d68c18a38b3bebb8bf48e9b13249b7
ARG CHEZMOI_SHA256_ARM64=abcb840401d3c1f2356e0f53f5d52aa10d10f572654d9626db9ad0ca4dc03355
ARG GH_VERSION=2.102.0
ARG GH_SHA256_AMD64=bb766f710eef8ede859c18578c72c327597cd4c8a85b06001b1f3843c6019386
ARG GH_SHA256_ARM64=7862c86c72f43df3a2d93ddde6f473285b4e2af61b494849846827e513ef6484
RUN set -eu; \
    case "$(uname -m)" in \
      x86_64)  arch=amd64; czarch=linux-musl_amd64; cz="$CHEZMOI_SHA256_AMD64"; ghs="$GH_SHA256_AMD64" ;; \
      aarch64) arch=arm64; czarch=linux_arm64;      cz="$CHEZMOI_SHA256_ARM64"; ghs="$GH_SHA256_ARM64" ;; \
      *) echo "unsupported arch $(uname -m)" >&2; exit 1 ;; \
    esac; \
    tmp=$(mktemp -d); \
    curl -fsSL -o "$tmp/chezmoi.tgz" \
      "https://github.com/twpayne/chezmoi/releases/download/v${CHEZMOI_VERSION}/chezmoi_${CHEZMOI_VERSION}_${czarch}.tar.gz"; \
    echo "${cz}  $tmp/chezmoi.tgz" | sha256sum -c -; \
    tar -xzf "$tmp/chezmoi.tgz" -C "$tmp" chezmoi; \
    install -m 0755 "$tmp/chezmoi" /usr/local/bin/chezmoi; \
    curl -fsSL -o "$tmp/gh.tgz" \
      "https://github.com/cli/cli/releases/download/v${GH_VERSION}/gh_${GH_VERSION}_linux_${arch}.tar.gz"; \
    echo "${ghs}  $tmp/gh.tgz" | sha256sum -c -; \
    tar -xzf "$tmp/gh.tgz" -C "$tmp" "gh_${GH_VERSION}_linux_${arch}/bin/gh"; \
    install -m 0755 "$tmp/gh_${GH_VERSION}_linux_${arch}/bin/gh" /usr/local/bin/gh; \
    rm -rf "$tmp"; \
    chezmoi --version; gh --version

# mise (version manager) is a pinned release binary checked against a
# SHA-256 recorded here, not `curl https://mise.run | sh`. Alpine's
# package is too old: it resolves the global python@3.12 below to the
# dotfiles' version instead. Bump MISE_VERSION with both sums (from the
# release's SHASUMS256.txt, cross-checked against the downloaded file).
# Runtimes installed via `mise install <lang>@<ver>` end up under
# /home/rousseau/.local/share/mise/, which is a bind-mounted volume
# in the Quadlet unit so cached toolchains survive image rebuilds.
ARG MISE_VERSION=2026.9.18
ARG MISE_SHA256_X64=2fd6b7eb9889f13e07dc3fdb6271c05815b81e03da86ff9f875de0b5c473afb8
ARG MISE_SHA256_ARM64=c415e01150d53746334e38d96d284f4c86953644b83d648855568f8fb9cc1e0a
RUN set -eu; \
    case "$(uname -m)" in \
      x86_64)  arch=x64;   sum="$MISE_SHA256_X64" ;; \
      aarch64) arch=arm64; sum="$MISE_SHA256_ARM64" ;; \
      *) echo "unsupported arch $(uname -m)" >&2; exit 1 ;; \
    esac; \
    curl -fsSL -o /usr/local/bin/mise \
      "https://github.com/jdx/mise/releases/download/v${MISE_VERSION}/mise-v${MISE_VERSION}-linux-${arch}-musl"; \
    echo "${sum}  /usr/local/bin/mise" | sha256sum -c -; \
    chmod 0755 /usr/local/bin/mise; \
    mise --version

# Install the claude CLI, pinned to the version the policy bridge
# (PreToolUse hook via --settings) was verified against. Bump only
# after re-checking that a denied tool call is blocked.
ARG CLAUDE_VERSION=2.1.285
# npm 12 blocks install scripts unless allowlisted; claude-code's
# postinstall fetches its native binary, so allow exactly that package.
RUN npm install -g --allow-scripts=@anthropic-ai/claude-code @anthropic-ai/claude-code@${CLAUDE_VERSION} \
 && node --version && claude --version

# Non-root user. UID/GID 1000 lines up with the typical single-user
# host so bind-mounts inherit ownership without extra chowning in
# rootless podman. The node:alpine base already ships a user at UID
# 1000; drop it first, then create rousseau at the same id.
RUN deluser --remove-home node 2>/dev/null || true \
 && delgroup node        2>/dev/null || true \
 && addgroup -g 1000 rousseau \
 && adduser  -u 1000 -G rousseau -h /home/rousseau -s /bin/bash -D rousseau

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
# ~/.ssh / ~/.gnupg symlinks are created LATER (after chezmoi init)
# — see the final RUN step. chezmoi's dotfiles include an
# ~/.ssh/config, so chezmoi mkdir'd ~/.ssh as a real directory and
# overwrote any symlink we tried to put there earlier.

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
#
# MISE_YES (accept license prompts, e.g. rust) applies to this build
# step only. Neither it nor MISE_TRUSTED_CONFIG_PATHS is set at
# runtime: either would auto-trust any repo's .mise.toml, and a
# trusted mise config can run arbitrary commands (hooks, tasks, env
# templates). Trust a repo explicitly with `mise trust <path>`, or set
# MISE_TRUSTED_CONFIG_PATHS in the Quadlet for paths you control.
# Node is already provided by the base image (node:22-alpine) — no
# need to have mise compile it from source (Alpine musl has no
# precompiled node@22 for mise, so `mise install node@22` would
# invoke a full V8 C++ compile that takes 30+ minutes). Per-repo
# `.mise.toml` can still ask mise for a different node if needed;
# it'll fetch on first use in the container's writable layer.
RUN MISE_YES=1 mise use -g python@3.12 go@1.26 rust@latest || true \
 && mise ls \
 && rm -rf /tmp/* /tmp/.[!.]* 2>/dev/null || true
# Clean /tmp so the runtime Tmpfs size=64m overlay doesn't fail with
# ENOSPC on container create. mise/python dumped build logs and
# compile caches here (~500 MB).

# Bootstrap chezmoi-managed dotfiles from the public GitHub source.
# --apply materialises them under $HOME; --no-tty forbids stdin prompts.
# Without a TTY the dotfiles' config template takes its values from
# CHEZMOI_<KEY> env vars (prompt flags are not consulted). Letting
# chezmoi generate its own config file (rather than pre-seeding one)
# keeps the [age] / [merge] sections in sync with the template's shape.
#
# No git identity is baked in: this image is published, and a baked-in
# name/email would make every deployment commit as the maintainer.
# Operators set theirs at runtime (git's GIT_CONFIG_COUNT/KEY/VALUE env
# vars; see docker/README.md "Git identity"). Until then git refuses to
# commit rather than committing as a placeholder.
RUN CHEZMOI_PROFILE=container \
    CHEZMOI_THEME=tokyonight-night \
    CHEZMOI_GIT_SIGNINGKEY='~/.ssh/id_ed25519' \
    CHEZMOI_GIT_SIGNINGFORMAT=ssh \
    chezmoi init --apply --no-tty --branch main \
      --promptString age_identity= \
      --promptString age_recipient= \
      sebastienrousseau \
      2>&1 | tail -30 \
 || echo "chezmoi bootstrap emitted warnings; container still usable"

# Final cleanup — same reason as the earlier /tmp wipe: keep the image
# lean and avoid tripping the runtime tmpfs size limit at container
# create.
RUN rm -rf /tmp/* /tmp/.[!.]* 2>/dev/null || true

# Point ~/.ssh and ~/.gnupg at the state-volume subtree the copy
# script populates on the host. Must run AFTER chezmoi init — chezmoi
# creates ~/.ssh as a real directory (for its ssh config templates)
# and would otherwise clobber the symlink.
#
# Anything chezmoi dropped inside ~/.ssh is discarded: the state
# volume is bind-mounted at container start, so whatever is baked
# into the image at ~/.local/share/rousseau/keys/ssh/ gets hidden
# by the host's copy anyway. The operator's real ssh config comes
# from the host via the copy script (see host-keys.list.example).
RUN rm -rf /home/rousseau/.ssh /home/rousseau/.gnupg \
 && ln -sfn /home/rousseau/.local/share/rousseau/keys/ssh   /home/rousseau/.ssh \
 && ln -sfn /home/rousseau/.local/share/rousseau/keys/gnupg /home/rousseau/.gnupg

# rousseau has no HTTP surface — no ports to expose. Health is the
# process being alive; podman restart policy handles crashes.
#
# The image ships without a hardcoded allowlist. Supply your JID via
# the ROUSSEAU_WHATSAPP_ALLOW env var, e.g.
#   docker run -e ROUSSEAU_WHATSAPP_ALLOW=15551234567@s.whatsapp.net rousseau
# or explicitly with `--allow` at container invocation.
ENTRYPOINT ["/usr/local/bin/rousseau"]
CMD ["whatsapp"]
