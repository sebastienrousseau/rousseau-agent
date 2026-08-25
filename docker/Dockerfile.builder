# syntax=docker/dockerfile:1.7
#
# agent-builder — polyglot build environment for the repo fleet.
#
# Threat model, stated explicitly because it differs from the daemon's:
#
#   This image gets a WRITABLE filesystem and a LARGE /tmp. In exchange
#   it holds NO long-lived credentials. No WhatsApp session, no Claude
#   OAuth token, no SSH signing key by default. Scoped credentials are
#   injected per-run via EnvironmentFile= in the Quadlet.
#
#   The daemon image is the mirror image of that trade: it keeps the
#   credentials and keeps ReadOnly=true. Neither image is ever both
#   privileged and credentialed. That property is the whole point of
#   the split — do not collapse these two images back together.
#
# Root is still not granted at runtime. Everything that needs elevation
# is installed at build time; the running container keeps
# NoNewPrivileges=true and DropCapability=all.
#
# Build:
#   podman build -t agent-builder:local -f docker/Dockerfile.builder .

ARG BASE_IMAGE=localhost/agent-base:local
FROM ${BASE_IMAGE} AS builder-env

USER root
ARG DEBIAN_FRONTEND=noninteractive

# ---------------------------------------------------------------------
# System toolchains.
#
# bubblewrap and socat are the load-bearing additions: they are the
# prerequisites for Claude Code's Linux sandbox. Without them the
# sandbox silently reports unavailable, and permission rules become the
# only boundary — which the upstream docs explicitly warn is fragile
# for network and filesystem containment.
#
# uidmap + fuse-overlayfs enable rootless podman-in-podman. Nested
# rootless containerisation additionally requires subuid/subgid ranges
# for the container user; see docker/agent-builder.container.
# ---------------------------------------------------------------------
RUN apt-get update -qq && apt-get install -y --no-install-recommends \
      `# --- sandbox prerequisites (Claude Code sandbox) ---` \
      bubblewrap \
      socat \
      `# --- C / C++ ---` \
      build-essential \
      clang \
      cmake \
      ninja-build \
      pkg-config \
      libssl-dev \
      `# --- interpreters not covered by mise ---` \
      lua5.4 \
      php-cli \
      `# --- developer CLI surface ---` \
      bat \
      direnv \
      fd-find \
      fzf \
      shellcheck \
      tmux \
      sqlite3 \
      `# --- nested containers (rootless) ---` \
      fuse-overlayfs \
      uidmap \
 && rm -rf /var/lib/apt/lists/* \
 && ln -sf /usr/bin/fdfind /usr/local/bin/fd \
 && ln -sf /usr/bin/batcat /usr/local/bin/bat

# ---------------------------------------------------------------------
# Pinned release binaries. Every checksum below was fetched from the
# upstream signed checksums file and verified against the downloaded
# artifact before being committed. Renovate keeps them current; do not
# hand-edit a version without re-fetching its hash.
#
# Refresh recipe (per tool):
#   curl -fsSL https://api.github.com/repos/<OWNER>/<REPO>/releases/latest | jq -r .tag_name
#   curl -fsSL https://github.com/<OWNER>/<REPO>/releases/download/<TAG>/<CHECKSUMS> | grep <ARTIFACT>
# ---------------------------------------------------------------------

# yq — YAML/JSON processor. Note: yq's `checksums` file is multi-column;
# SHA-256 is the 18th hash column (field $19), per checksums_hashes_order.
ARG YQ_VERSION=4.53.6
ARG YQ_SHA256=c5f056448f973ae7d39b5401949648a78f2dc1947d6a8eb65be60d5c504b9385
RUN set -eu \
 && url="https://github.com/mikefarah/yq/releases/download/v${YQ_VERSION}/yq_linux_amd64" \
 && curl -fsSL "$url" -o /tmp/yq \
 && echo "${YQ_SHA256}  /tmp/yq" | sha256sum -c - \
 && install -m 0755 /tmp/yq /usr/local/bin/yq \
 && rm -f /tmp/yq

# syft — SBOM generation.
ARG SYFT_VERSION=1.51.0
ARG SYFT_SHA256=2a2e837a2c8d59ec9af5472ee22d3b04ee463c4e44476ecf993fd1e5ab6ebc7f
RUN set -eu \
 && url="https://github.com/anchore/syft/releases/download/v${SYFT_VERSION}/syft_${SYFT_VERSION}_linux_amd64.tar.gz" \
 && curl -fsSL "$url" -o /tmp/syft.tar.gz \
 && echo "${SYFT_SHA256}  /tmp/syft.tar.gz" | sha256sum -c - \
 && tar -xzf /tmp/syft.tar.gz -C /tmp syft \
 && install -m 0755 /tmp/syft /usr/local/bin/syft \
 && rm -f /tmp/syft /tmp/syft.tar.gz

# grype — vulnerability scanning.
ARG GRYPE_VERSION=0.117.0
ARG GRYPE_SHA256=38525dab1e06f162ebaa02f94d82d1f807076b011a44180cf2777edf1a7b9c26
RUN set -eu \
 && url="https://github.com/anchore/grype/releases/download/v${GRYPE_VERSION}/grype_${GRYPE_VERSION}_linux_amd64.tar.gz" \
 && curl -fsSL "$url" -o /tmp/grype.tar.gz \
 && echo "${GRYPE_SHA256}  /tmp/grype.tar.gz" | sha256sum -c - \
 && tar -xzf /tmp/grype.tar.gz -C /tmp grype \
 && install -m 0755 /tmp/grype /usr/local/bin/grype \
 && rm -f /tmp/grype /tmp/grype.tar.gz

# act — run GitHub Actions workflows locally. Closes the "no local CI"
# gap: fleet workflow changes can be validated before hitting 192 repos.
ARG ACT_VERSION=0.2.89
ARG ACT_SHA256=0191d6f1f3b716b5c55820032605d05fc3c1cdbf581ebeff655019e5dd1524c0
RUN set -eu \
 && url="https://github.com/nektos/act/releases/download/v${ACT_VERSION}/act_Linux_x86_64.tar.gz" \
 && curl -fsSL "$url" -o /tmp/act.tar.gz \
 && echo "${ACT_SHA256}  /tmp/act.tar.gz" | sha256sum -c - \
 && tar -xzf /tmp/act.tar.gz -C /tmp act \
 && install -m 0755 /tmp/act /usr/local/bin/act \
 && rm -f /tmp/act /tmp/act.tar.gz

# ---------------------------------------------------------------------
# Swift — opt-in.
#
# Deliberately NOT enabled by default: the toolchain is ~700 MiB and
# only 10 of 192 fleet repos need it. Enable with
# --build-arg INSTALL_SWIFT=true, and supply a real SWIFT_SHA256.
#
# The build FAILS CLOSED if SWIFT_SHA256 is left empty. That is
# intentional — an unpinned toolchain download would defeat the
# supply-chain posture of every other layer in this file.
#
# Refresh recipe:
#   https://www.swift.org/install/linux/  -> pick the Ubuntu 24.04 tarball
#   curl -fsSL <URL>.sha256
# ---------------------------------------------------------------------
ARG INSTALL_SWIFT=false
ARG SWIFT_VERSION=
ARG SWIFT_SHA256=
RUN set -eu; \
    if [ "${INSTALL_SWIFT}" = "true" ]; then \
      if [ -z "${SWIFT_SHA256}" ] || [ -z "${SWIFT_VERSION}" ]; then \
        echo "ERROR: INSTALL_SWIFT=true requires SWIFT_VERSION and SWIFT_SHA256." >&2; \
        echo "       Refusing to install an unpinned toolchain." >&2; \
        exit 1; \
      fi; \
      apt-get update -qq && apt-get install -y --no-install-recommends \
        binutils libcurl4-openssl-dev libpython3-dev libxml2-dev \
        zlib1g-dev tzdata; \
      rm -rf /var/lib/apt/lists/*; \
      url="https://download.swift.org/swift-${SWIFT_VERSION}-release/ubuntu2404/swift-${SWIFT_VERSION}-RELEASE/swift-${SWIFT_VERSION}-RELEASE-ubuntu24.04.tar.gz"; \
      curl -fsSL "$url" -o /tmp/swift.tar.gz; \
      echo "${SWIFT_SHA256}  /tmp/swift.tar.gz" | sha256sum -c -; \
      mkdir -p /opt/swift; \
      tar -xzf /tmp/swift.tar.gz -C /opt/swift --strip-components=1; \
      ln -sf /opt/swift/usr/bin/swift /usr/local/bin/swift; \
      rm -f /tmp/swift.tar.gz; \
    else \
      echo "Swift install skipped (INSTALL_SWIFT=${INSTALL_SWIFT})"; \
    fi

# ---------------------------------------------------------------------
# Language runtimes via mise, installed at BUILD time.
#
# This is the fix for the runtime failure mode observed in the Alpine
# image: mise had auto_install=true with a 3s remote-version timeout,
# so every shim invocation emitted warning spew to stderr and no tool
# ever actually installed. Baking the toolchains in removes both the
# network dependency and the stderr pollution at run time.
# ---------------------------------------------------------------------
USER rousseau

ARG MISE_NODE=24
ARG MISE_PYTHON=3.12
ARG MISE_GO=latest
ARG MISE_RUST=latest
ARG MISE_RUBY=3.3
ARG MISE_JAVA=21

ENV MISE_FETCH_REMOTE_VERSIONS_TIMEOUT=30s \
    RUSTUP_HOME=/home/rousseau/.rustup \
    CARGO_HOME=/home/rousseau/.cargo

# ---------------------------------------------------------------------
# Container override for the dotfiles-managed mise base layer.
#
# ~/.config/mise/conf.d/00-dotfiles.toml pins every build cache under
# /tmp/builds — GOCACHE, GOTMPDIR, PIP_CACHE_DIR, UV_CACHE_DIR and both
# ZIG_*_CACHE_DIRs. That is correct on the CachyOS host, where /tmp is
# backed by real disk. It is actively broken in a container, where /tmp
# is a small tmpfs: a `go build` of this repo exhausts a 64 MiB /tmp
# partway through linking and fails with "no space left on device",
# and the same applies to pip and zig.
#
# conf.d files load in lexical order with later files winning, so a
# 10-prefixed file overrides the 00-prefixed dotfiles base without
# editing a chezmoi-managed file. Caches move to $HOME, which is
# writable in this image and backed by the container's overlay.
#
# mise's [env] values are applied by the shim at exec time and override
# process environment, so this cannot be worked around with `export`
# at call sites — it has to be fixed in config.
# ---------------------------------------------------------------------
RUN mkdir -p /home/rousseau/.config/mise/conf.d \
 && cat > /home/rousseau/.config/mise/conf.d/10-container.toml <<'TOML'
# Container override — see docker/Dockerfile.builder for rationale.
# /tmp is a small tmpfs here; build caches must not live there.
[env]
GOCACHE = "/home/rousseau/.cache/go-build"
GOTMPDIR = "/home/rousseau/.cache/go-tmp"
GOMODCACHE = "/home/rousseau/go/pkg/mod"
PIP_CACHE_DIR = "/home/rousseau/.cache/pip"
UV_CACHE_DIR = "/home/rousseau/.cache/uv"
ZIG_LOCAL_CACHE_DIR = "/home/rousseau/.cache/zig"
ZIG_GLOBAL_CACHE_DIR = "/home/rousseau/.cache/zig-global"
TOML
RUN mkdir -p /home/rousseau/.cache/go-build /home/rousseau/.cache/go-tmp \
             /home/rousseau/.cache/pip /home/rousseau/.cache/uv \
             /home/rousseau/go/pkg/mod

RUN set -eu \
 && mise use -g -y \
      node@${MISE_NODE} \
      python@${MISE_PYTHON} \
      go@${MISE_GO} \
      rust@${MISE_RUST} \
      ruby@${MISE_RUBY} \
      java@${MISE_JAVA} \
 && mise reshim \
 && mise ls --installed

# Python tooling that the fleet's 45 Python repos expect. Installed with
# --user into the (now writable) home rather than system-wide.
RUN set -eu \
 && python3 -m pip install --no-cache-dir --user \
      pre-commit \
      semgrep \
 && echo "pip --user install OK"

# ---------------------------------------------------------------------
# Build-time self-test. The image fails to build if any headline
# capability is missing, so a regression is caught in CI rather than
# discovered by an agent mid-run three weeks later.
# ---------------------------------------------------------------------
RUN set -eu; \
    fail=0; \
    for t in bwrap socat clang cmake ninja yq syft grype act fd bat \
             node python3 go cargo ruby java lua5.4 php; do \
      if command -v "$t" >/dev/null 2>&1; then \
        printf '  ok      %s\n' "$t"; \
      else \
        printf '  MISSING %s\n' "$t"; fail=1; \
      fi; \
    done; \
    printf '  npm prefix = %s\n' "$(npm config get prefix)"; \
    [ "$(npm config get prefix)" = "/home/rousseau/.node_modules" ] \
      || { echo '  MISSING npm prefix override (B5)'; fail=1; }; \
    gocache="$(go env GOCACHE)"; gotmp="$(go env GOTMPDIR)"; \
    printf '  GOCACHE  = %s\n  GOTMPDIR = %s\n' "$gocache" "$gotmp"; \
    case "$gocache" in /tmp/*) echo '  GOCACHE still on tmpfs (mise 00-dotfiles not overridden)'; fail=1;; esac; \
    case "$gotmp"   in /tmp/*) echo '  GOTMPDIR still on tmpfs (mise 00-dotfiles not overridden)'; fail=1;; esac; \
    bwrap --ro-bind / / -- /bin/true \
      && echo '  ok      bwrap can create a sandbox' \
      || { echo '  bwrap present but cannot create a sandbox (nested userns?)'; fail=1; }; \
    [ "$fail" -eq 0 ] || { echo 'builder self-test FAILED'; exit 1; }; \
    echo 'builder self-test PASSED'

WORKDIR /workspace
CMD ["/bin/bash"]
