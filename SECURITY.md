# Security Policy

## Reporting a vulnerability

Report privately to **sebastian.rousseau@gmail.com**. Do not open a public issue for security-affecting reports.

Include:

- A concise description and a severity assessment (CVSS 3.1 vector preferred).
- The affected component identified by file path and line range (e.g. `internal/tools/builtin/bash.go:42-60`) or by module path if the issue is in a dependency.
- Environment details (`rousseau version` output, Go version, OS, container runtime).
- A minimal reproduction, ideally as a failing test or a self-contained script.
- If applicable, the CVE or GHSA identifier of an upstream advisory.

### Response commitments

| Event | SLA |
|---|---|
| Acknowledgment of report | ≤ 72 hours |
| Triage decision (accept / decline / need-info) | ≤ 7 days |
| Fix landed for **Critical** (CVSS ≥ 9.0) | ≤ 14 days |
| Fix landed for **High** (7.0–8.9) | ≤ 30 days |
| Fix landed for **Medium / Low** | scheduled in a routine release |
| Public disclosure (coordinated) | after fix release; credit to reporter unless declined |

## Supported versions

Only the `main` branch and the most recent tagged release receive security fixes. There are no long-term support branches.

## Trust model

### In scope

The `rousseau-agent` process is a **local, container-native daemon**. Its load-bearing security boundaries are:

1. **The user's shell.** The `bash` built-in tool executes arbitrary commands with the user's privileges. Every tool call is surfaced before execution and is subject to the configured approval policy (`allow_all`, `deny_all`, or `pattern` mode with per-tool regex allow / deny rules and a configurable default). Operators running unattended (chat-transport) daemons **must** either enforce `pattern` mode with a `default: deny` fallback or accept `bypassPermissions` posture with an explicit understanding of the exposure.
2. **Container isolation.** The reference deployment is a rootless Podman container with `ReadOnly=true`, `DropCapability=all`, `NoNewPrivileges=true`, a default seccomp filter, a non-root user (UID 1000), and `keep-id` user-namespace mapping. Only the workspace bind mount, the state directory, and `~/.claude` are visible from inside the container.
3. **Supply chain of the binary.** See below.

### Out of scope

- **Malicious model output.** The operator is responsible for reviewing tool calls before approving them. Approval policies exist to make this less error-prone; they do not eliminate the need for human judgment.
- **Compromised Go toolchain, container runtime, or host OS.** A trustworthy build environment is assumed.
- **Physical access to the machine running `rousseau`.**
- **Attacks against the LLM provider itself.** Provider vulnerabilities are that provider's responsibility.

## Supply-chain hardening

| Control | Implementation |
|---|---|
| Direct dependency pinning | Exact versions in `go.mod`; transitive resolution frozen in `go.sum`. |
| Vulnerability scanning | `govulncheck ./...` runs on every CI build. Builds fail on any known vulnerability that reaches an imported symbol. |
| Static analysis | `golangci-lint` v2 (18 linters) + GitHub CodeQL (Go). |
| Dependency updates | Dependabot for `gomod` and `github-actions` groups; weekly cadence. |
| Build provenance | **SLSA Level 3** via `slsa-framework/slsa-github-generator`. Provenance is attested through GitHub Actions OIDC and published to the Sigstore transparency log. |
| Release signing | Release checksums are signed with **cosign** (keyless, via GitHub Actions OIDC). |
| Software bill of materials | **CycloneDX JSON** SBOM attached to every release artifact. |
| Reproducible builds | Dedicated `reproducible-build` CI job verifies bit-identical output from fresh checkouts. |

Verifying a downloaded release:

```bash
cosign verify-blob \
  --certificate-identity-regexp 'sebastienrousseau/rousseau-agent' \
  --certificate-oidc-issuer 'https://token.actions.githubusercontent.com' \
  --signature rousseau_<version>_checksums.txt.sig \
  rousseau_<version>_checksums.txt

sha256sum -c rousseau_<version>_checksums.txt
```

## Cryptography inventory

| Use | Implementation |
|---|---|
| TLS to LLM / transport endpoints | Go standard library `crypto/tls` (system trust store) |
| WhatsApp | `whatsmeow` (Signal protocol) |
| Matrix | Client-server API over HTTPS |
| SMTP (email transport) | Go standard library `net/smtp` with `PlainAuth` over TLS |
| Session store at rest | Not encrypted at the application layer. Operators requiring encryption at rest should mount the state directory on an encrypted filesystem (LUKS, `cryptsetup`, FileVault). |

No custom cryptographic primitives are implemented in this project.

## Coordinated disclosure

For issues affecting an upstream dependency, please also report to that dependency's maintainers. If you are reporting an issue that requires coordinated disclosure across multiple projects, indicate this in your initial report so we can align embargo timelines.
