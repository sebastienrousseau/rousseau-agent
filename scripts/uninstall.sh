#!/usr/bin/env bash
# rousseau-agent uninstaller. Reverses scripts/install.sh.
#
# Preserves state by default. Pass `--purge` to also delete
# ~/.local/share/rousseau (WhatsApp pairing, session DB, cron history).

set -euo pipefail

PURGE=0
for arg in "$@"; do
    case "$arg" in
        --purge)  PURGE=1 ;;
        --help|-h)
            cat <<EOF
Usage: uninstall.sh [--purge]

  --purge   Also wipe ~/.local/share/rousseau (irreversible).
EOF
            exit 0 ;;
        *) echo "unknown arg: $arg" >&2; exit 2 ;;
    esac
done

say() { printf '\033[1;35m▸\033[0m %s\n' "$*"; }

say 'stopping rousseau-agent.service'
systemctl --user disable --now rousseau-agent.service 2>/dev/null || true

say 'removing Quadlet unit'
rm -f "$HOME/.config/containers/systemd/rousseau-agent.container"
systemctl --user daemon-reload

say 'removing container + image'
podman rm -f rousseau-agent 2>/dev/null || true
podman rmi localhost/rousseau-agent:local 2>/dev/null || true

if [ "$PURGE" -eq 1 ]; then
    say 'wiping ~/.local/share/rousseau'
    rm -rf "$HOME/.local/share/rousseau"
fi

say 'done'
