# rousseau-agent — first-run walkthrough

**Goal:** empty terminal → running WhatsApp bridge in under five minutes.

**Assumptions:** you have `podman >= 5`, `git`, and either the `claude` CLI on `$PATH` (default) or an Anthropic API key. Everything else is fetched by the install script.

---

## Step 1 — Install

```bash
curl -sSL https://raw.githubusercontent.com/sebastienrousseau/rousseau-agent/main/scripts/install.sh | bash
```

**What you should see:**

```
▸ checking prerequisites
▸ cloning https://github.com/sebastienrousseau/rousseau-agent into ~/.local/share/rousseau/src
▸ building container image rousseau-agent:local
STEP 1/9: FROM golang:1.26-alpine AS builder
…
Successfully tagged localhost/rousseau-agent:local
▸ creating state directories
▸ installing Quadlet unit into ~/.config/containers/systemd
▸ reloading systemd + starting rousseau-agent

rousseau-agent installed.

  Tail the log to see the WhatsApp QR (first launch only):
      journalctl --user -u rousseau-agent.service -f
```

**If it errors:** the script tells you exactly which prerequisite is missing. Re-run after installing it.

---

## Step 2 — Interactive setup (optional)

If you'd rather not edit YAML by hand:

```bash
rousseau init
```

**What you should see:**

```
rousseau init

Which provider should the agent use?
  [1] claudecli  — shell out to the local `claude` CLI (default; no API key)
  [2] anthropic  — direct Anthropic API
  [3] openai     — OpenAI Chat Completions
  [4] openrouter — any model via OpenRouter (OpenAI shim)
  [5] ollama     — local ollama endpoint
  [6] bedrock    — AWS Bedrock (Anthropic Claude via SigV4)
provider [1]: 1
workspace path [~/team-rousseau-workspace]: <Enter>
WhatsApp allowlist JID (blank to skip WhatsApp): 15551234567@s.whatsapp.net
Telegram bot token (blank to skip Telegram): <Enter>

✓ wrote ~/.config/rousseau/config.yaml

Next steps:
  1. `rousseau whatsapp --allow 15551234567@s.whatsapp.net`  (first launch prints a QR code)
  2. `rousseau status`  (verify state DB, cron jobs)
  3. `rousseau doctor`  (full diagnostic sweep)
```

---

## Step 3 — Pair WhatsApp

Tail the log to catch the QR:

```bash
journalctl --user -u rousseau-agent.service -f
```

A block of ASCII QR renders as the first-launch WhatsApp Web pairing begins. From your phone:

**WhatsApp → Settings → Linked devices → Link a device → scan the QR.**

Log continues with:

```
Successfully paired 15551234567:21@s.whatsapp.net
whatsapp.paired
whatsapp.connected
```

---

## Step 4 — Send yourself a message

From your phone, message your own number (or a linked device):

> "Hello agent, can you list the files in my workspace?"

**On your phone,** you'll see the WhatsApp contact status change to **"typing…"**, then a reply headed with **💎 *Rousseau Agent***. The container ran the model, executed the read tool inside its sandbox, and answered.

Verify the incoming leg from the daemon logs:

```
whatsapp.incoming from=15551234567@s.whatsapp.net
whatsapp.handler_ok elapsed=3.4s reply_len=213
```

---

## Step 5 — Verify state

```bash
rousseau status
```

**What you should see:**

```
rousseau 4d89cc1 (commit 4d89cc1, built 2026-07-12T09:12:00Z)

state.path             /home/you/.local/share/rousseau/sessions.db
state.size             48.00 KiB
sessions               1
cron.jobs              0 (enabled=0)
transport.jid_mappings 1
claude.cached_sessions 1
last_activity_at       2026-07-12T09:15:04Z (2m14s ago)
```

---

## Step 6 — Schedule a daily brief

```bash
rousseau cron add \
    --name morning-brief \
    --schedule "0 8 * * *" \
    --prompt "Summarise my calendar and top 3 unread emails" \
    --deliver-to 15551234567@s.whatsapp.net
```

**Every morning at 08:00 UTC** the daemon runs the prompt, ships the result to WhatsApp. `rousseau cron list` shows the queue; `rousseau cron disable morning-brief` pauses it.

---

## Common failures

| Symptom | Fix |
|---|---|
| `podman >= 5 required` | Update podman via your distro's package manager. |
| `claude CLI not on $PATH` | Install Claude Code; or switch to `provider: anthropic` in the config. |
| Silent hang after `whatsapp.incoming` | Missing `claudecli.permission_mode: bypassPermissions` in config. |
| `SQLITE_BUSY` during first pairing | Fixed in `55fdee3` — pull latest. |

---

## Cleanup

```bash
# Reversible: stop the daemon and remove the container. Preserves state.
scripts/uninstall.sh

# Nuclear: also wipe ~/.local/share/rousseau (WhatsApp pairing gone).
scripts/uninstall.sh --purge
```

That's the whole loop.
