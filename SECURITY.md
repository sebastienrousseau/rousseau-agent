# Security Policy

## Reporting a Vulnerability

Report privately to **sebastian.rousseau@gmail.com**. Do not open a public issue.

Include:

- A concise description and severity assessment.
- The affected component identified by file path and line range (e.g. `internal/tools/builtin/bash.go:42-60`).
- Environment details (`rousseau version` output, Go version, OS).
- A minimal reproduction, ideally as a failing test.

Expect an acknowledgment within 72 hours.

## Trust model

`rousseau-agent` is a local CLI. Its one load-bearing security boundary is:

**The user's shell.** The `bash` tool executes arbitrary commands with the user's privileges. Every tool call is surfaced in the TUI before execution; enable `--auto-approve` at your own risk.

Out of scope:

- Malicious model output. The user is responsible for reviewing tool calls before approving them.
- Compromised Go toolchain / OS. We assume a trustworthy build environment.
- Physical access to the machine running `rousseau`.

## Supply chain

- Direct dependencies are pinned to exact versions in `go.mod`.
- `govulncheck` runs on every CI build.
- `dependabot` opens PRs for security-relevant updates.
