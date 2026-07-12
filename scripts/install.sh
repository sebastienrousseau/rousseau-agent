#!/usr/bin/env bash
# rousseau-agent installer.
#
# One-line install:
#     curl -sSL https://raw.githubusercontent.com/sebastienrousseau/rousseau-agent/main/scripts/install.sh | bash
#
# Prerequisites the script does NOT install for you:
#   - podman (>= 5.0)
#   - claude CLI on $PATH (or set provider=anthropic in the config and
#     export ANTHROPIC_API_KEY)
#
# The script clones the repo, builds a rootless Podman image, installs
# a systemd Quadlet unit, and starts the daemon. WhatsApp pairing is
# left to the user — the first `journalctl` line shows the QR.

set -euo pipefail

# ---------------------------- config -----------------------------------
REPO_URL="${ROUSSEAU_REPO:-https://github.com/sebastienrousseau/rousseau-agent.git}"
REPO_REF="${ROUSSEAU_REF:-main}"
INSTALL_DIR="${ROUSSEAU_HOME:-$HOME/.local/share/rousseau/src}"
IMAGE_TAG="${ROUSSEAU_IMAGE:-rousseau-agent:local}"
SYSTEMD_DIR="$HOME/.config/containers/systemd"
STATE_DIR="$HOME/.local/share/rousseau"
CLAUDE_DIR="$HOME/.claude"
WORKSPACE_DIR="${ROUSSEAU_WORKSPACE:-$HOME/team-rousseau-workspace}"

# ---------------------------- helpers ----------------------------------
say()   { printf '\033[1;35m▸\033[0m %s\n' "$*"; }
warn()  { printf '\033[1;33m!\033[0m %s\n' "$*" >&2; }
fatal() { printf '\033[1;31m✘\033[0m %s\n' "$*" >&2; exit 1; }

need() {
    command -v "$1" >/dev/null 2>&1 || fatal "$1 is required but not on \$PATH"
}

# ---------------------------- checks -----------------------------------
say 'checking prerequisites'
need git
need podman
if ! command -v claude >/dev/null 2>&1; then
    warn 'claude CLI not on $PATH — the container defaults to provider=claudecli.'
    warn 'Install Claude Code first, or set provider=anthropic in ~/.config/rousseau/config.yaml.'
fi

podman_major=$(podman --version | awk '{print $3}' | cut -d. -f1)
if [ "$podman_major" -lt 5 ]; then
    fatal "podman >= 5 required (found $podman_major). Install a newer podman first."
fi

# ---------------------------- clone / update ---------------------------
if [ -d "$INSTALL_DIR/.git" ]; then
    say "updating $INSTALL_DIR"
    git -C "$INSTALL_DIR" fetch --depth=1 origin "$REPO_REF"
    git -C "$INSTALL_DIR" checkout FETCH_HEAD
else
    say "cloning $REPO_URL into $INSTALL_DIR"
    mkdir -p "$(dirname "$INSTALL_DIR")"
    git clone --depth=1 --branch "$REPO_REF" "$REPO_URL" "$INSTALL_DIR"
fi

# ---------------------------- build image ------------------------------
say "building container image $IMAGE_TAG"
podman build \
    --build-arg VERSION="$(git -C "$INSTALL_DIR" rev-parse --short HEAD)" \
    --build-arg COMMIT="$(git -C "$INSTALL_DIR" rev-parse HEAD)" \
    --build-arg BUILD_DATE="$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    -t "$IMAGE_TAG" \
    -f "$INSTALL_DIR/docker/Dockerfile" \
    "$INSTALL_DIR"

# ---------------------------- state directories ------------------------
say 'creating state directories'
mkdir -p "$STATE_DIR"
mkdir -p "$WORKSPACE_DIR"
mkdir -p "$SYSTEMD_DIR"
mkdir -p "$CLAUDE_DIR"

# ---------------------------- Quadlet unit -----------------------------
say "installing Quadlet unit into $SYSTEMD_DIR"
UNIT_SRC="$INSTALL_DIR/docker/rousseau-agent.container"
UNIT_DST="$SYSTEMD_DIR/rousseau-agent.container"
if [ ! -f "$UNIT_SRC" ]; then
    fatal "Quadlet template not found at $UNIT_SRC"
fi
# Rewrite the workspace path so users who cloned to a non-default
# location still get a working unit.
sed "s|%h/team-rousseau-workspace|${WORKSPACE_DIR}|" "$UNIT_SRC" > "$UNIT_DST"

# ---------------------------- start ------------------------------------
say 'reloading systemd + starting rousseau-agent'
systemctl --user daemon-reload
systemctl --user enable --now rousseau-agent.service 2>/dev/null || \
    systemctl --user start rousseau-agent.service

# ---------------------------- next steps -------------------------------
cat <<EOF

$(tput bold 2>/dev/null || :)rousseau-agent installed.$(tput sgr0 2>/dev/null || :)

  Tail the log to see the WhatsApp QR (first launch only):
      journalctl --user -u rousseau-agent.service -f

  Once paired, message yourself on WhatsApp — replies come from
  the container. To reconfigure, edit:
      ~/.config/rousseau/config.yaml       (see docs/COMPETITORS.md)
      ~/.config/containers/systemd/rousseau-agent.container

  Uninstall with scripts/uninstall.sh in the cloned repo.
EOF
