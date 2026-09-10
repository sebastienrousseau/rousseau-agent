#!/usr/bin/env bash
# rousseau-agent installer — binary edition.
#
# One-line install (recommended for prosumer / first-time users):
#
#     curl -sSL https://raw.githubusercontent.com/sebastienrousseau/rousseau-agent/main/scripts/install.sh | bash
#
# Downloads the pre-built binary for your OS + architecture from the
# GitHub Releases page, verifies its SHA-256 against the release's
# checksums.txt, verifies the cosign signature when cosign is on PATH,
# and installs it to $HOME/.local/bin/rousseau.
#
# What this script installs:
#   - The rousseau binary at $HOME/.local/bin/rousseau (~50 MB)
#
# What this script does NOT install (deliberate scope cut):
#   - Podman + the container Quadlet unit — use scripts/install-from-
#     source.sh for the full container-native production layout.
#   - Claude CLI — install separately if you want the claudecli
#     provider. rousseau also supports Anthropic API, OpenAI,
#     Bedrock, Vertex, and any OpenAI-compatible endpoint.
#   - Any daemon config — run `rousseau setup` after install for
#     the interactive first-run wizard.
#
# Time-to-first-message target: 5 minutes on a fresh Ubuntu, macOS,
# or Fedora box. Report bugs against this target as a regression.

set -euo pipefail

# --------------------------- config ---------------------------------

# Override to install a specific version instead of latest.
ROUSSEAU_VERSION="${ROUSSEAU_VERSION:-latest}"

# Override to point at a fork or private mirror.
ROUSSEAU_REPO="${ROUSSEAU_REPO:-sebastienrousseau/rousseau-agent}"

# Override to install to a different directory (defaults to
# $HOME/.local/bin — the XDG convention, and already on $PATH on
# most modern distros).
INSTALL_DIR="${ROUSSEAU_INSTALL_DIR:-$HOME/.local/bin}"

# Skip the cosign check even when cosign is present (e.g. for
# reproducible offline installs where you've already verified out
# of band).
ROUSSEAU_SKIP_COSIGN="${ROUSSEAU_SKIP_COSIGN:-}"

# --------------------------- helpers --------------------------------

say()   { printf '\033[1;35m▸\033[0m %s\n' "$*"; }
warn()  { printf '\033[1;33m!\033[0m %s\n' "$*" >&2; }
fatal() { printf '\033[1;31m✘\033[0m %s\n' "$*" >&2; exit 1; }
ok()    { printf '\033[1;32m✓\033[0m %s\n' "$*"; }

need() {
    command -v "$1" >/dev/null 2>&1 || fatal "$1 is required but not on \$PATH"
}

# detect_os returns the goreleaser-normalised OS name.
detect_os() {
    case "$(uname -s)" in
        Linux)   echo "linux" ;;
        Darwin)  echo "darwin" ;;
        MINGW*|MSYS*|CYGWIN*)
            fatal 'Windows via WSL is supported (use Linux install); native Windows install is not implemented — download the .zip from the Releases page manually.'
            ;;
        *) fatal "unsupported OS: $(uname -s)" ;;
    esac
}

# detect_arch returns the goreleaser-normalised architecture name.
# Includes GOARM suffix for 32-bit ARM so the correct binary lands.
detect_arch() {
    case "$(uname -m)" in
        x86_64|amd64) echo "amd64" ;;
        aarch64|arm64) echo "arm64" ;;
        armv7l|armv7)  echo "armv7" ;;
        armv6l|armv6)  echo "armv6" ;;
        riscv64)       echo "riscv64" ;;
        *) fatal "unsupported architecture: $(uname -m)" ;;
    esac
}

# resolve_version turns "latest" into the concrete release tag by
# hitting the GitHub API. Any explicit "vX.Y.Z" is passed through.
resolve_version() {
    if [ "$ROUSSEAU_VERSION" != "latest" ]; then
        echo "$ROUSSEAU_VERSION"
        return
    fi
    local url="https://api.github.com/repos/${ROUSSEAU_REPO}/releases/latest"
    # Prefer curl (in the one-liner install context, always
    # present). Fall back to wget.
    local tag
    if command -v curl >/dev/null 2>&1; then
        tag=$(curl -fsSL "$url" | grep -oE '"tag_name":\s*"[^"]+"' | head -1 | sed -E 's/.*"([^"]+)"/\1/')
    else
        tag=$(wget -qO- "$url" | grep -oE '"tag_name":\s*"[^"]+"' | head -1 | sed -E 's/.*"([^"]+)"/\1/')
    fi
    if [ -z "$tag" ]; then
        fatal "could not resolve latest release from $url"
    fi
    echo "$tag"
}

# fetch downloads $2 to $1, preferring curl then wget. Fails
# fast — an install script that silently downloaded 0 bytes and
# blessed it as the binary would be worse than a clear error.
fetch() {
    local dst="$1" url="$2"
    if command -v curl >/dev/null 2>&1; then
        curl -fsSL --output "$dst" "$url" || fatal "download failed: $url"
    else
        wget -qO "$dst" "$url" || fatal "download failed: $url"
    fi
}

# --------------------------- main -----------------------------------

# Sanity check the environment.
say 'checking prerequisites'
need uname
need mkdir
need mv
if ! command -v curl >/dev/null 2>&1 && ! command -v wget >/dev/null 2>&1; then
    fatal 'either curl or wget is required to download the binary'
fi
if ! command -v sha256sum >/dev/null 2>&1 && ! command -v shasum >/dev/null 2>&1; then
    fatal 'either sha256sum (Linux) or shasum (macOS) is required for checksum verification'
fi
if ! command -v tar >/dev/null 2>&1; then
    fatal 'tar is required to extract the release archive'
fi

OS=$(detect_os)
ARCH=$(detect_arch)
VERSION=$(resolve_version)
VERSION_NO_V="${VERSION#v}"

say "detected: os=$OS arch=$ARCH version=$VERSION"

# Goreleaser's default archive template is
# `{project}_{version}_{os}_{arch}{arm-suffix}.tar.gz` (see
# .goreleaser.yaml → archives.name_template). Match exactly.
ASSET_NAME="rousseau-agent_${VERSION_NO_V}_${OS}_${ARCH}.tar.gz"
BASE_URL="https://github.com/${ROUSSEAU_REPO}/releases/download/${VERSION}"
ASSET_URL="${BASE_URL}/${ASSET_NAME}"
CHECKSUMS_URL="${BASE_URL}/checksums.txt"

# Work in a per-run tempdir so an aborted install leaves nothing
# behind. Cleanup on any exit (success or failure).
TMPDIR=$(mktemp -d)
trap 'rm -rf "$TMPDIR"' EXIT

say "downloading $ASSET_NAME"
fetch "$TMPDIR/$ASSET_NAME" "$ASSET_URL"
fetch "$TMPDIR/checksums.txt" "$CHECKSUMS_URL"

# --------------------------- verify ---------------------------------

say 'verifying SHA-256 checksum'
cd "$TMPDIR"

# GNU sha256sum on Linux, BSD `shasum -a 256` on macOS. Both accept
# `--check` reading a "<sha>  <file>" list, but the file MUST
# contain only the assets present locally, otherwise the checker
# reports missing-file failures. Grep down to just the one line
# we care about.
if ! grep -F "$ASSET_NAME" checksums.txt > checksums.local.txt; then
    fatal "checksums.txt does not list $ASSET_NAME — release may be incomplete"
fi

if command -v sha256sum >/dev/null 2>&1; then
    sha256sum --check --strict checksums.local.txt >/dev/null || \
        fatal "SHA-256 checksum mismatch for $ASSET_NAME"
else
    # macOS `shasum -a 256 -c` is compatible with the same file.
    shasum -a 256 -c checksums.local.txt >/dev/null || \
        fatal "SHA-256 checksum mismatch for $ASSET_NAME"
fi
ok 'SHA-256 checksum verified'

# --------------------------- cosign (optional) ----------------------

if [ -z "$ROUSSEAU_SKIP_COSIGN" ] && command -v cosign >/dev/null 2>&1; then
    # goreleaser publishes .sig files alongside the release
    # tarball when a cosign key is configured (see .goreleaser.yaml
    # signs: section). Fetch and verify keylessly against the
    # transparency log — matches how the container images are
    # signed.
    if fetch "$TMPDIR/checksums.txt.sig" "${BASE_URL}/checksums.txt.sig" 2>/dev/null && \
       fetch "$TMPDIR/checksums.txt.pem" "${BASE_URL}/checksums.txt.pem" 2>/dev/null; then
        say 'verifying cosign signature (keyless, transparency log)'
        if cosign verify-blob \
              --certificate "$TMPDIR/checksums.txt.pem" \
              --signature   "$TMPDIR/checksums.txt.sig" \
              --certificate-identity-regexp 'https://github.com/.*rousseau-agent' \
              --certificate-oidc-issuer     'https://token.actions.githubusercontent.com' \
              "$TMPDIR/checksums.txt" >/dev/null 2>&1; then
            ok 'cosign signature verified'
        else
            warn 'cosign verification failed — the download passed SHA-256 but the release signature is not attributable to the expected identity. Investigate before running the binary.'
            fatal 'aborting install due to cosign failure — override with ROUSSEAU_SKIP_COSIGN=1 if you have out-of-band trust'
        fi
    fi
else
    if [ -n "${ROUSSEAU_SKIP_COSIGN}" ]; then
        warn 'cosign verification skipped by request (ROUSSEAU_SKIP_COSIGN set)'
    else
        warn 'cosign not on $PATH — SHA-256 verified but cosign signature not checked. Install cosign for supply-chain attestation: https://docs.sigstore.dev/system_config/installation/'
    fi
fi

# --------------------------- install --------------------------------

say "extracting to $TMPDIR"
tar -xzf "$ASSET_NAME"
if [ ! -f "$TMPDIR/rousseau" ]; then
    fatal 'expected binary `rousseau` inside archive was not present — release may be malformed'
fi

say "installing to $INSTALL_DIR"
mkdir -p "$INSTALL_DIR"
mv "$TMPDIR/rousseau" "$INSTALL_DIR/rousseau"
chmod +x "$INSTALL_DIR/rousseau"

# --------------------------- next steps -----------------------------

PATH_HINT=''
if ! command -v rousseau >/dev/null 2>&1 || [ "$(command -v rousseau)" != "$INSTALL_DIR/rousseau" ]; then
    PATH_HINT=$'\n  Add '"$INSTALL_DIR"$' to your $PATH (bash / zsh):\n      echo '"'"'export PATH="'"$INSTALL_DIR"$':$PATH"'"'"' >> ~/.bashrc && source ~/.bashrc\n'
fi

cat <<EOF

$(tput bold 2>/dev/null || :)rousseau-agent $VERSION installed.$(tput sgr0 2>/dev/null || :)
$PATH_HINT
  First-run wizard (writes ~/.config/rousseau/config.yaml and
  guides you through provider + transport setup):

      $INSTALL_DIR/rousseau setup

  Or jump straight to a chat transport once configured:

      $INSTALL_DIR/rousseau whatsapp        # scan the QR from your phone
      $INSTALL_DIR/rousseau chat            # local TUI

  For the full container-native production layout (Podman +
  systemd Quadlet), run scripts/install-from-source.sh instead
  after cloning the repo.

  Docs:      https://github.com/${ROUSSEAU_REPO}/tree/main/docs
  Compliance & buyer notes: docs/BUYER.md
EOF
