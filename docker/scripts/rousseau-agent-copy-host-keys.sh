#!/usr/bin/env bash
# rousseau-agent-copy-host-keys — populate the container's key
# staging area from an explicit host-side allowlist.
#
# WHY THIS EXISTS
#
# The rousseau-agent container runs claude-cli with
# --permission-mode bypassPermissions, so a bind mount of ~/.ssh or
# ~/.gnupg gives an unattended agent full read access to the host's
# key material — a prompt-injection-driven `cat` would exfiltrate.
# Instead of a direct mount, this script copies only the specific
# files the operator names in a curated list into the container's
# state volume, which is already bind-mounted.
#
# The container-side symlinks (baked into the Dockerfile) point
# ~/.ssh and ~/.gnupg at that state subtree, so applications see
# files where they expect them.
#
# CROSS-PLATFORM
#
# Pure POSIX-ish bash. Uses only cp -p, mkdir -p, and shell
# builtins — works on Linux + macOS + WSL without change. The
# calling systemd unit is Linux-only (podman quadlet); to run on
# another host, invoke this script from whatever init the target
# uses before starting the container.
#
# CONFIG FORMAT
#
# One absolute path per line (~/ tilde is expanded). '#' comments
# and blank lines are ignored. Only files under $HOME/.ssh/ or
# $HOME/.gnupg/ are allowed — anything else is skipped with a
# diagnostic, so a typo can't reach into unrelated dotfiles.
#
#     ~/.ssh/id_ed25519
#     ~/.ssh/id_ed25519.pub
#     ~/.ssh/known_hosts
#     ~/.ssh/config
#     # ~/.gnupg/pubring.kbx     (uncomment if you use GPG signing)
#     # ~/.gnupg/trustdb.gpg
#
# INSTALL
#
#     install -m 0755 docker/scripts/rousseau-agent-copy-host-keys.sh \
#                     ~/.local/bin/rousseau-agent-copy-host-keys
#     install -m 0644 -D docker/host-keys.list.example \
#                     ~/.config/rousseau/host-keys.list
#     # …edit ~/.config/rousseau/host-keys.list, then restart the agent

set -euo pipefail

CONFIG="${ROUSSEAU_KEYS_LIST:-$HOME/.config/rousseau/host-keys.list}"
DEST_ROOT="${ROUSSEAU_KEYS_DEST:-$HOME/.local/share/rousseau/keys}"

log() { printf 'rousseau-copy-host-keys: %s\n' "$*" >&2; }

if [ ! -f "$CONFIG" ]; then
    log "no config at $CONFIG — nothing to copy"
    exit 0
fi

mkdir -p "$DEST_ROOT"

copied=0
skipped=0
while IFS= read -r raw || [ -n "$raw" ]; do
    line="${raw%%#*}"
    # Trim surrounding whitespace without depending on GNU-only tools.
    line="${line#"${line%%[![:space:]]*}"}"
    line="${line%"${line##*[![:space:]]}"}"
    [ -z "$line" ] && continue

    # Expand ~ and $VAR references, then resolve to an absolute path
    # if possible. eval is safe here — the config file is user-owned.
    # shellcheck disable=SC2086
    src="$(eval printf '%s' $line)"

    if [ ! -e "$src" ]; then
        log "skip missing $src"
        skipped=$((skipped + 1))
        continue
    fi
    if [ ! -f "$src" ]; then
        log "skip $src (not a regular file)"
        skipped=$((skipped + 1))
        continue
    fi

    # Path safety: only $HOME/.ssh/ or $HOME/.gnupg/ subtrees.
    case "$src" in
        "$HOME/.ssh/"*)   rel="ssh/${src#"$HOME/.ssh/"}" ;;
        "$HOME/.gnupg/"*) rel="gnupg/${src#"$HOME/.gnupg/"}" ;;
        *)
            log "skip $src (only \$HOME/.ssh/ or \$HOME/.gnupg/ subtrees are allowed)"
            skipped=$((skipped + 1))
            continue
            ;;
    esac

    dst="$DEST_ROOT/$rel"
    mkdir -p "$(dirname "$dst")"
    # -p preserves mode and mtime; -f overwrites without prompting.
    # Together these mean the container sees the file with the same
    # permissions the host applies (typically 0600 for private keys).
    cp -pf "$src" "$dst"
    copied=$((copied + 1))
done < "$CONFIG"

# Match the tightened default GNU keyring perms (700 on dirs, 600 on
# files) — some SSH clients refuse to use a key whose parent dir is
# group- or world-readable.
find "$DEST_ROOT" -type d -exec chmod 0700 {} +
find "$DEST_ROOT" -type f -exec chmod 0600 {} +
# Public keys can stay world-readable; the container still opens them
# read-only. `.pub` and `known_hosts` / `config` are safe to relax.
find "$DEST_ROOT" \( -name '*.pub' -o -name 'known_hosts' -o -name 'config' \) \
    -type f -exec chmod 0644 {} +

log "copied=$copied skipped=$skipped into $DEST_ROOT"
