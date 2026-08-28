#!/usr/bin/env bash
# Unattended demo script for `asciinema rec`. Timed so a human viewer
# can read each block before the next command lands.
#
# NOT idempotent — this actually runs the install! Only use inside a
# throwaway VM or container. `docker run --rm -it ubuntu:24.04 bash`
# is the reference environment for recording.

set -euo pipefail

# ------------------------- helpers -------------------------------------
type() {
    local msg=$1
    for ((i=0; i<${#msg}; i++)); do
        printf '%s' "${msg:$i:1}"
        sleep 0.02
    done
    printf '\n'
}
pause() { sleep "${1:-2}"; }
step()  { printf '\n\033[1;35m▸ %s\033[0m\n' "$1"; pause 1; }

# ------------------------- 0. banner -----------------------------------
clear
cat <<'EOF'
────────────────────────────────────────────────────────────────────
   rousseau-agent: install to first reply in 5 minutes
────────────────────────────────────────────────────────────────────
EOF
pause 3

# ------------------------- 1. install ----------------------------------
step 'One-line install'
type '$ curl -sSL https://raw.githubusercontent.com/sebastienrousseau/rousseau-agent/main/scripts/install.sh | bash'
pause 1
# The real invocation goes here when the recording is inside a
# throwaway VM. For the demo asciicast we simulate the output.
cat <<'EOF'
▸ checking prerequisites
▸ cloning https://github.com/sebastienrousseau/rousseau-agent
▸ building container image rousseau-agent:local  (~30s)
[1/2] STEP 1/9: FROM golang:1.26-alpine …
[2/2] STEP 11/11: CMD ["whatsapp", "--allow", "…"]
Successfully tagged localhost/rousseau-agent:local
▸ creating state directories
▸ installing Quadlet unit
▸ reloading systemd + starting rousseau-agent

rousseau-agent installed. Tail with:
    journalctl --user -u rousseau-agent.service -f
EOF
pause 4

# ------------------------- 2. interactive config -----------------------
step 'Interactive setup'
type '$ rousseau init'
pause 1
cat <<'EOF'
rousseau init

Which provider should the agent use?
  [1] claudecli  — shell out to the local `claude` CLI (default; no API key)
  [2] anthropic  — direct Anthropic API
  …
provider [1]: 1
workspace path [~/team-rousseau-workspace]:
WhatsApp allowlist JID (blank to skip WhatsApp): 15551234567@s.whatsapp.net
Telegram bot token (blank to skip Telegram):

✓ wrote ~/.config/rousseau/config.yaml

Next steps:
  1. `rousseau whatsapp --allow 15551234567@s.whatsapp.net`
  2. `rousseau status`
  3. `rousseau doctor`
EOF
pause 4

# ------------------------- 3. pair WhatsApp ----------------------------
step 'Pair WhatsApp — scan the QR from your phone'
type '$ journalctl --user -u rousseau-agent.service -f'
pause 1
cat <<'EOF'
level=INFO msg=whatsapp.starting store=file:…/whatsapp.db allowlist=1
level=INFO msg=whatsapp.qr_ready
█████████████████████████████████████████████████████████████████
██ ▄▄▄▄▄ █▀ █▀██ ▄▄▄▄▄ ██ … [QR code, scan from phone] … ██
█████████████████████████████████████████████████████████████████
level=INFO msg=whatsapp.paired
level=INFO msg=whatsapp.connected
EOF
pause 6

# ------------------------- 4. first message ----------------------------
step 'Send yourself a message (from another linked device)'
cat <<'EOF'

  📱 You  :  Hello agent, can you list the files in my workspace?
  💎 Agent:  (typing…)
  💎 Agent:  ✨ Rousseau Agent

              Sure — the workspace root contains:
              - README.md
              - src/ (12 files)
              - docs/ (4 files)
              - go.mod, go.sum

              Anything specific you'd like me to open?

level=INFO msg=whatsapp.incoming from=15551234567@s.whatsapp.net
level=INFO msg=whatsapp.handler_ok elapsed=3.4s reply_len=213
EOF
pause 6

# ------------------------- 5. status -----------------------------------
step 'Verify state'
type '$ rousseau status'
pause 1
cat <<'EOF'
rousseau 4d89cc1 (commit 4d89cc1, built 2026-07-12T09:12:00Z)

state.path             /home/you/.local/share/rousseau/sessions.db
state.size             48.00 KiB
sessions               1
cron.jobs              0 (enabled=0)
transport.jid_mappings 1
claude.cached_sessions 1
last_activity_at       2026-07-12T09:15:04Z (2m14s ago)
EOF
pause 4

# ------------------------- 6. schedule ---------------------------------
step 'Schedule a daily brief'
type '$ rousseau cron add --name morning-brief --schedule "0 8 * * *" \\
    --prompt "Summarise my calendar and top 3 unread emails" \\
    --deliver-to 15551234567@s.whatsapp.net'
pause 1
cat <<'EOF'
added morning-brief (0 8 * * *) → 3f4e1a2b-5c6d-7890-abcd-ef1234567890
EOF
pause 4

# ------------------------- 7. wrap -------------------------------------
step 'Done'
cat <<'EOF'

You now have:
  • A rootless containerised WhatsApp bridge with drop-all-caps + seccomp
  • Automated daily briefs at 08:00 UTC delivered to your phone
  • Full transcript history queryable via `rousseau session search "…"`
  • MCP endpoint at `rousseau mcp` (attach to Claude Code / Cursor)

  Docs:      https://github.com/sebastienrousseau/rousseau-agent
  Uninstall: scripts/uninstall.sh   (or --purge for full wipe)
EOF
pause 5
