---
name: podman-quadlet
description: Design or debug a rootless Podman Quadlet container unit, matching rousseau-agent's own hardening posture.
triggers: [quadlet, podman container, rootless podman, systemd container, .container file]
---

When the user asks about a Podman Quadlet unit:

**Baseline hardening — apply unless the user explicitly opts out:**

```ini
[Container]
UserNS=keep-id             # host UID/GID visible inside; bind-mounts stay owned as expected
Network=pasta              # rootless egress-only, faster than slirp4netns
ReadOnly=true              # writable areas must be Volume= or Tmpfs=
NoNewPrivileges=true
DropCapability=all         # explicitly re-add via AddCapability= when needed
SeccompProfile=/usr/share/containers/seccomp.json
AutoUpdate=disabled        # no silent :latest pulls; require an explicit `podman pull`
```

**When mounting host state, prefer targeted binds:**
- Full `~/.claude:/home/rousseau/.claude:rw,Z` drags in years of
  session history and slows Claude Code cold-start; instead mount an
  isolated `~/.claude-agent/` and overlay just `.credentials.json`.
- Read-only `~/.ssh:/home/rousseau/.ssh:ro,Z` for signing keys.
- `%h/.config/rousseau/agent.env` for secret env — `EnvironmentFile=`
  keeps `.container` itself in git.

**When forwarding an SSH agent** into the container, mount the
directory (not the socket file) so the mount survives agent restarts
that recreate the socket inode:

```ini
Volume=%t/gcr:/run/host-agent:rw
Environment=SSH_AUTH_SOCK=/run/host-agent/ssh
```

**When the container needs to publish a port** (rare — rousseau's
transports are all outbound), prefer a Unix socket over `PublishPort`.
Rootless Podman requires ports > 1024 unless the operator has set
`net.ipv4.ip_unprivileged_port_start` via sysctl.

**Debugging:**
- `systemctl --user status <unit>` — is it running?
- `journalctl --user -u <unit> -n 100 --no-pager` — recent output.
- `systemctl --user daemon-reload` after editing the .container file.
- `podman inspect <container>` — resolved mounts, env, caps at runtime.
- `podman logs <container>` — same as journalctl when using SystemdUnit.
