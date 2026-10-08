# Changelog

All notable changes to `rousseau-agent` are documented here.

Versioning follows the project convention: every release increments by
exactly `+0.0.1`. `v0.0.1` is the first tag; `v0.1.0` only follows
`v0.0.999`; `v1.0.0` only follows `v0.999.999`. The version number
carries no semantic-version meaning — breaking-change intent is
signalled in the release-notes entry, not in the version string.

The format is loosely based on
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); commit
messages follow Conventional Commits (`feat:`, `fix:`, `refactor:`,
`docs:`, `test:`, `chore:`, `ci:`).

## [Unreleased]

### Security (v0.0.13)

Fixes from the 2026-10-08 security audit.

- **Recall is scoped to the sender (High).** Automatic recall searched
  every session and put other senders' titles and snippets into the
  current sender's system prompt, where the model could repeat them.
  Recall now searches only sessions whose sender equals the current
  session's sender (local `rousseau chat` sessions form their own
  scope), and recalled text is wrapped in an untrusted-data fence the
  snippets cannot close. Vector recall returns nothing unless it can
  check each hit's owner. **API change:** `RecallSearcher.Search` (and
  `pkg/state/sqlite`'s `RecallSearcher`) now takes the sender; stores
  gain `SearchScoped`.

- **Email sender authentication can no longer be forged (High).** The
  Authentication-Results gate accepted any header carrying
  `dkim=pass`, including one the sender wrote, and was off by default,
  so a forged `From:` of an allowlisted address got a full agent turn.
  Only headers whose authserv-id is `email.trusted_authserv_id` now
  count, and only the topmost of them; DMARC decides when present.
  **Breaking:** an email allowlist without
  `require_authentication_results` + `trusted_authserv_id` refuses to
  start (exit 78); `email.insecure_trust_from: true` keeps the old
  behaviour for test inboxes. `rousseau doctor` reports the state.

- **Multi-party approval no longer bypasses policy (High).** For a
  tool covered by a multi-party rule, the vote replaced the inner
  approver chain, so RBAC, OPA, the risk judge and pattern deny rules
  never ran, and voters saw only the tool name. The inner chain now
  runs first and a deny ends the request; requests carry a digest and
  summary of the exact input. **Chat change:** `/pending` lists open
  requests and `/approve <token> <digest>` must quote the first 8
  characters of the input digest. The audit record gains
  `input_sha256` and `input_summary`.

- **The claude CLI no longer inherits the daemon's secrets (Medium).**
  The claude child process got the daemon's full environment (every
  channel token and provider key) while running its own Bash tool, so
  a prompt injection could read them with `env`. It now gets the
  envscrub baseline plus `ANTHROPIC_*`, `CLAUDE_*`, and proxy and CA
  settings. **Breaking for some setups:** variables claude's own MCP
  servers or Bedrock/Vertex modes need (e.g. `AWS_*`, `GOOGLE_*`) go in
  `claudecli.env_passthrough`.

- Tool error text is now capped like tool output
  (`agent.max_tool_output_bytes`), so a tool that echoes a large body
  into its error cannot flood the context.

- **Daemon file tools are confined to a workspace by default
  (Medium).** With `tools.fs.root` unset, read, write, edit and grep
  could reach all of `$HOME` except a short deny list. The daemon now
  defaults the root to `$XDG_DATA_HOME/rousseau/workspace` (interactive
  `rousseau chat` is unchanged). The deny list adds GitHub CLI, gcloud,
  Azure, npm, PyPI and Cargo credentials, `pass`, shell histories and
  the audit chain key; a new write-only list blocks shell start-up
  files, systemd user units, autostart entries, `~/.local/bin`,
  LaunchAgents and any `.git/hooks` path. Matching is case-insensitive
  on macOS and Windows. **Breaking:** set `tools.fs.root: "/"` to keep
  unrestricted daemon file tools.

- The Gmail send tool rejects CR/LF in `to`, `subject` and `from`,
  parses addresses, and RFC 2047-encodes the subject, so a model-
  supplied subject can no longer inject a hidden `Bcc:` header
  (Medium).

- **grep stays inside the guard (Medium).** Only grep's starting
  directory went through the guard; the walk then descended freely (a
  search from `$HOME` read `~/.ssh`) and opened symlinked files that
  pointed out of the workspace. Every directory and file is now
  checked, symlinks, FIFOs and devices are never opened, and total
  output is capped at 64 KiB (512 bytes per line). `read` no longer
  blocks when pointed at a FIFO.

- Bash output is capped while the command runs (1 MiB) instead of
  after it finishes, and a timeout kills the whole process group, so a
  backgrounded child can neither keep the call blocked nor survive it.
  Applies to direct execution and the sandbox backends.
- `spawn_subagent` can only lower operator limits: the model's token
  budget and concurrency are clamped to the operator's, the task count,
  turns and timeout are enforced server-side, and a task's system text
  is appended to the parent's prompt instead of replacing it.
- MCP: tools with invalid names (outside `[A-Za-z0-9_.-]{1,64}`) are
  skipped; descriptions are capped at 2 KiB, schemas over 64 KiB fall
  back to the permissive schema, and error bodies in tool errors are
  capped at 4 KiB. A server line over the 1 MiB limit, or a server that
  exits, now fails pending and later calls at once instead of after the
  request timeout.

- **File tools cannot be raced out of the workspace (Medium).** read,
  grep, write and edit now open and write beneath the workspace root
  through Go's `os.Root`, so a directory swapped for a symlink between
  the path check and the file operation (for example by a bash loop)
  cannot redirect a read or a write outside the root. Writes are
  atomic (temp file, sync, rename), keep an existing file's
  permissions, refuse read-only files, and create a missing root
  0700. `edit` reads with the same size cap and regular-file check as
  `read` (L-4).

- **Outbound integration tools need approval by default (Low).** With
  no approver configured (`allow_all`), the daemon ran tools that send
  data to third parties (GitHub issues and comments, Gmail send,
  Calendar events, Slack posts and reactions, Linear writes, every
  Composio action) without asking. They are now denied unless an
  approver allows them; `agent.approver.allow_outbound: true` restores
  the old behaviour. `rousseau doctor` and `rousseau evidence` report
  the posture. `a2a_dispatch` is outbound too; MCP tools are not yet
  classified.

- **gVisor counts as a sandbox only with filesystem isolation
  (Medium).** The gVisor backend ran `runsc do` with the host's `/` as
  the container root, so bash commands could read `$HOME` and every
  secret on the host while the daemon reported itself sandboxed. Each
  run now gets a private root holding only the configured read-only
  and writable mounts, with writes kept in a memory overlay. A mount of
  `/`, `$HOME` or a parent of `$HOME` is refused at start-up and on
  every run. **Breaking:** runsc release-20250611.0 or later is
  required, and writes to gVisor `writable` paths no longer reach the
  host.

- **Group conversations are off by default and get their own sessions
  (Medium).** In a Telegram group, Slack channel, Discord server,
  Signal group, Matrix room or iMessage group chat, the agent answered
  an allowlisted member from that member's private session, so its
  history, recall and tool output reached every member. Group messages
  are now ignored, control verbs included, unless
  `<transport>.allow_groups: true` is set. When it is, each (sender,
  conversation) pair gets its own session and turn, so private history
  never reaches a group. Turns in a group are not resumed after a
  restart. **Breaking:** bots used in groups need `allow_groups: true`.

- **Email ignores auto-replies and bounces (Low).** An out-of-office
  reply or a bounce from an allowlisted address started an agent turn,
  and the agent's reply could start another, in a loop. Mail with
  `Auto-Submitted` (other than `no`), `Precedence: bulk|list|junk|
  auto_reply`, `List-Id` or `List-Unsubscribe`, or from
  `MAILER-DAEMON`/`postmaster`, is now dropped, and replies carry
  `Auto-Submitted: auto-replied`. The body given to the agent is the
  first `text/plain` MIME part, decoded and capped at 256 KiB, instead
  of the raw message source.

- **SSO login is bound to the handle, single-use and DM-only
  (Medium).** `/login` bound any handle that pasted a valid token, so
  a token seen in a group, a log or a screenshot signed its finder in
  as the token's owner, and an issuer's tokens for any other client
  were accepted when no audience was set. Now a token whose transport
  claim names a handle binds only that handle, each token signs in
  once (keyed on `jti`, or a digest of the signed input, in a new
  `sso_spent_tokens` table on SQLite and Postgres), and `/login` in a
  group binds nothing. **Breaking:** `sso.oidc.audience` is required;
  `sso.oidc.allow_any_audience: true` is the explicit opt-out.

- **Pattern rules match canonical, per-field input (Medium).** Deny
  patterns matched the raw JSON text, so `{"command":"rm -rf /"}`,
  a repeated key or a differently cased key ran a denied command.
  Tool input is now canonicalised (escapes decoded, keys unique and
  case-folded, numbers exact) before rules and tools see it, input
  with repeated keys is denied, and a rule can match one `field`,
  fully anchored. Allow rules match the tool name exactly.
  `rousseau doctor` warns on unanchored allow patterns.

- **Hook-rewritten input is approved again (Medium).** A PreToolUse
  hook that answered "modify" replaced the input after approval, and
  a later hook's deny could be lost. Rewritten input now goes back
  through the approver, a deny from any hook wins, a rewrite that is
  not valid JSON is denied, and `hooks[].fail_closed: true` denies
  when a hook fails or times out. Hook timeouts are now enforced when
  a hook's child keeps its output open.

- **Log redaction catches every secret (Medium).** The redacting log
  handler stopped at the first matching rule, skipped the log message
  and matched `LogValuer` attributes before resolving them, and it
  missed current token formats. Every rule now runs over attribute
  values and the message, values are resolved first, and rules cover
  OpenAI project/service keys, Slack `xoxp-`/`xoxe-`, GitHub
  `gho_`/`ghs_`/`ghu_`/`ghr_`, Telegram bot tokens, Google `ya29.`
  tokens, Bearer headers, PEM private keys and URL passwords.

- **Bot tokens and passwords stay out of logged URL errors (Medium).**
  Telegram puts the bot token in the request path and BlueBubbles the
  password in the query, and a network error quoted the full URL into
  the log. Those URLs are now redacted in every request error.

- **Error details and secrets stay out of chat (Low).** Cron failures,
  failed turns and failed tools posted the full error, which can carry
  child-process output, into chat. Chat now shows "failed (ref …)"
  and the log holds the detail under the same ref. Progress bullets
  pass through the redactor. `rousseau doctor` redacts every Postgres
  DSN form, keyword and query-string passwords included (L-11).

- **Transport input checks (Info).** Every transport marks direct
  conversations (`IncomingMessage.IsDirect`). Telegram drops messages
  sent as a channel or by an anonymous group admin, which share one
  sender ID across Telegram (I-1). Signal refuses attachment IDs that
  are paths (S-12). Slack sends its bot token only to
  `https://files.slack.com`, refuses cross-host redirects and treats
  an oversized file as an error (S-13).

- **/link needs confirmation and /unlink needs ownership (Low).** Any
  sender could `/link` a handle they did not control to their identity
  or `/unlink` someone else's handle. `/link <transport>:<sender>` now
  issues a six-digit code, valid 10 minutes, that the target handle
  must send back with `/confirm <code>`; a wrong code cancels the
  request. `/unlink` only removes a handle linked to the caller's own
  identity, and both reject unknown transports.

- **Strangers get no replies (Low).** A sender outside the allowlist
  got answers from control verbs (`/status`, `/cancel`), rate-limit
  notices and some command errors, which confirmed the bot existed.
  Everything now stays silent for them except `/login` when SSO is on.
  `/logout` without a binding replies "not signed in" and writes no
  audit record.

- **The release image no longer runs a second repository's code
  (High).** `docker/Dockerfile` ran `chezmoi init --apply` against the
  dotfiles repository's `main` branch at build time, so whatever was
  there, `run_` scripts included, executed inside every signed and
  attested image, and builds were not reproducible from the tag. The
  release image now gets a reviewed `.gitconfig` instead and ships no
  chezmoi. The dev image (`Dockerfile.builder`) fetches the dotfiles at
  a pinned commit with checksum checks; `scripts/dotfiles-pin.sh` and a
  monthly workflow keep the pin current.

- **A2A tasks belong to the peer that sent them (Medium).** Only
  `POST /tasks` recorded the authenticated peer; `/message:send` and
  JSON-RPC fell back to the body's `from_agent`, so a token holder
  could act as any other peer, and any peer could read, follow or
  cancel another's task. Every route now records the authenticated
  peer as the task owner, other peers get 404, and a reused task ID is
  refused (409) instead of replacing the running task.

- **The A2A agent card is signed only for a configured URL (Medium).**
  The card URL came from `Host` / `X-Forwarded-Host` and was signed
  with the operator's key, so a forged header got a validly signed
  card pointing at an attacker. Cards are signed only when
  `a2a.server.public_url` is set and always advertise it; forwarded
  headers count only from `a2a.server.trusted_proxies`. **Breaking:**
  without `public_url` the card is served unsigned.

- **A2A and SCIM listen on loopback unless TLS is configured
  (Medium).** The A2A server listened on `:8443` in plaintext with no
  body or idle timeouts and no limit on concurrent tasks. The default
  bind is now `127.0.0.1:8443`; a non-loopback A2A or SCIM bind without
  `tls_cert_file` + `tls_key_file` refuses to start unless
  `allow_plaintext: true`. Requests get read and idle deadlines, and a
  peer may run 4 tasks at once (32 in total, `max_inflight_per_peer` /
  `max_inflight`); more get 429. **Breaking:** off-host plaintext
  binds no longer start.

- A2A hardening (Low): bearer tokens are kept only as SHA-256 digests
  and compared in constant time; peers and `/readyz` get "failed (ref
  …)" instead of internal error text; a client that trusts publisher
  keys no longer falls back to the unsigned legacy card on a 404; the
  default artifact fetcher refuses loopback, private, link-local
  (cloud metadata) and CGNAT addresses and ignores proxy variables.

- **Release pipeline (Medium).** `install.sh` refuses to install when
  a release's signature is missing (`ROUSSEAU_SKIP_COSIGN=1`
  overrides) and accepts only this repository's `release.yml` at the
  exact tag as signer, not any fork. `container-release` jobs get only
  the permissions they use. The DCO exemption for Dependabot keys on
  the PR opener's account, not a free-text author name. Dispatch
  inputs reach workflow scripts as validated environment variables
  instead of being pasted into them. Release builds no longer restore
  caches written by main or PR runs.

- **Provenance covers what is shipped (Low).** SLSA provenance
  attested four binaries a separate workflow rebuilt, never the
  archives on the release page. `release.yml` now attests the
  GoReleaser archives themselves, checked against `checksums.txt`;
  `slsa.yml` is removed. Image bases are pinned by multi-arch digest,
  toolchains by exact version, builder Python packages by hash, and
  Dependabot watches the Dockerfiles.

- **Helm chart tracks the release (Low).** The chart's appVersion was
  v0.0.3, the default full image ran under the distroless UID, the
  probes hit a metrics port the daemon never opened, and the mounted
  config was never read. The chart is now 0.0.2 for v0.0.13, defaults
  to the distroless image (`image.flavour`), sets `metrics_addr` and
  `--config`, and has an optional NetworkPolicy; a release fails if
  appVersion does not match the tag. **Breaking:** the full image
  needs `image.flavour=full` and UID/GID 1000.

- **Audit chain v2 (Low).** Chain fields were joined with a NUL byte,
  so two different records could hash alike, and an exported boot
  record could never verify. New records use a length-prefixed,
  versioned encoding with canonical detail JSON; existing v1 chains
  still verify. `rousseau audit verify <file>` checks an OTLP-JSON
  export from a SIEM or collector.

- **The audit chain key must be owner-only (Low).** A key readable by
  group or others now stops startup; the configured key file and
  `$XDG_STATE_HOME/rousseau` are denied to file tools; `rousseau
  evidence` fingerprints the key the daemon actually uses.
  **Breaking:** `chmod 0600` the key (podman secrets: `mode=0400`).

- Erasure and search (Low): `session delete-by-sender` also clears the
  interrupted-turn journal on both drivers and lists SQLite migration
  backups that still hold the erased data. Recall quotes each search
  term, so words like `don't` no longer break it. Session search and
  the MCP session tools clamp limits to 1..200 (default 20); the MCP
  list tool no longer returns every session when no limit is given.
  Skill signatures are checked over the bytes that were loaded, not a
  second read of the file.

- **Sign-in and approval (Low).** The OAuth loopback flow uses PKCE,
  ignores stray callbacks and no longer logs the auth URL. OIDC keys
  are used only for their declared `alg` and `use`, discovery and JWKS
  are capped at 1 MiB, `jwks_uri` must be https on the issuer's host
  (or `sso.oidc.jwks_allowed_hosts`), and failed refreshes back off
  while the last good keys keep working. Tool calls the claude CLI
  makes are judged as the user whose turn caused them, and a stalled
  toolgate client times out. Multi-party rules can name
  `approver_groups`; only members may vote. An expired licence now
  switches licensed features off at runtime and sets
  `rousseau_license_valid` to 0.

### Phase 1: trust and cost (2026-10-06)

Security and correctness work from the October 2026 audit. Three
entries change behaviour for existing deployments.

- **BREAKING: unattended daemons refuse to start with an unsandboxed
  bash tool** unless `tools.bash.sandbox.allow_unsandboxed: true` (or
  a sandbox kind) is set. Same refusal shape as `claudecli.permission_mode`.
- **BREAKING: subprocess environment is scrubbed.** bash commands,
  sandbox backends and MCP servers inherit only PATH, HOME, locale,
  TERM and scratch-dir variables. Name extra variables (or `PREFIX*`)
  in `tools.bash.env_passthrough` / `mcp.clients.<name>.env_passthrough`.
- **BREAKING: Telegram identity is the sending user, not the chat.**
  Group allow-lists keyed by chat id must switch to user ids. Private
  chats are unaffected. A2A sessions are keyed by the authenticated
  peer (`a2a/tok:<fingerprint>`), not the body's `from_agent`.
- File tools (`read`, `write`, `edit`, `grep`) are confined by a
  symlink-resolved deny list (daemon config and state, `~/.ssh`,
  `~/.gnupg`, `~/.aws`, `~/.kube`, `~/.docker`, `~/.claude`, `/proc`,
  `/sys`, `/dev`) and an optional workspace root (`tools.fs.root`,
  `tools.fs.deny`). `read` refuses non-regular files and caps at 4 MiB.
- Prompt caching is now active on the Anthropic provider; tool
  schemas forward `required` and other JSON-Schema keywords.
- `${VAR}` references in config are expanded (unset is an error); an
  explicit `--config` path that does not exist is an error; config
  failures exit 78.
- Unknown tool names and mid-tool cancellation produce tool_results
  instead of leaving a dangling `tool_use`; each tool execution is
  bounded by `agent.tool_timeout` (default 10m) and its output capped at
  `agent.max_tool_output_bytes` (default 64 KiB) with a truncation marker.
- Session compression also triggers on an estimated prompt size
  (`agent.compression.trigger_tokens`, default 120000), so a few
  messages carrying large tool outputs are condensed before they
  overflow the context window.
- `rousseau evidence` emits a compliance evidence pack: build stamp,
  config hash and summary, licence state, audit-chain head and HMAC key
  fingerprint, effective controls, retention settings, the doctor
  report and, with `--daemon`, readiness and `rousseau_*` metrics, with
  each field mapped to the EU AI Act, GDPR, DORA, SOC 2 and HIPAA
  articles in `docs/compliance/`. The pack holds no secrets.
- MCP client: `tools/list` follows `nextCursor`, so servers that page
  their tools no longer lose everything after the first page; a `ping`
  from the server is answered and other server requests get "method
  not found" instead of silence; and a tool result with `isError`
  passes the server's error text to the model instead of a generic
  "server reported error".
- MCP client speaks protocol revision 2026-07-28: it probes each server
  with `server/discover` and, when the server answers, sends the version
  and client info in every request's `_meta` with no `initialize`.
  Other servers fall back to `initialize`, now offering 2025-11-25 and
  accepting 2025-06-18, 2025-03-26 or 2024-11-05; any other negotiated
  version is refused. Abandoned requests send `notifications/cancelled`.
- MCP server (`rousseau mcp`) speaks protocol revision 2026-07-28: it
  answers `server/discover`, serves requests that carry the version in
  `_meta` without `initialize` (results marked `resultType: complete`,
  `tools/list` with cache hints), and refuses other stateless versions
  with -32022 and the supported list. `initialize` now echoes the
  client's version when it is 2025-11-25, 2025-06-18, 2025-03-26 or
  2024-11-05 instead of always answering 2024-11-05. Unknown
  notifications are no longer answered with an error.
- Durable turns: the agent saves the session after every complete
  iteration, and the router saves the sender's message before the turn
  and the session after a failed, timed-out or cancelled turn. Before,
  only a successful turn was saved, so a failure lost the message and
  every tool call that had already run. The restart notice now lists
  the tool calls that ran before the interruption, read from that
  checkpoint.
- `agent.resume_interrupted` (off by default) continues a turn a
  restart cut off from its last checkpoint and delivers the reply,
  marked as resumed; a turn that had finished but whose reply was never
  delivered is re-sent without a model call. If resuming fails, the
  sender gets the notice listing what had run.
- The provider router (`provider: router`) now streams: a turn is
  streamed from the routed child, and a child without streaming is
  replayed as one text delta. Before, routing turned token streaming
  off for every provider behind it.
- The Postgres driver now supports GDPR erasure (`session
  delete-by-sender`, with bare-identifier resolution), `state.session_ttl`
  retention, and the interrupted-turn journal, which were SQLite-only.
  `reliability` and `eval` read and write persisted samples on either
  driver.
- Transports carry the conversation, message and thread ids of each
  inbound message (`IncomingMessage.Conversation/MessageID/Thread`);
  Slack and Telegram answer inside the thread or topic the message came
  from, email replies thread under the inbound Message-ID with a
  "Re: " subject, and Signal group messages are answered in the group
  instead of by direct message. Replies longer than a platform cap are
  split into several messages (Telegram 4096, Discord 2000, Slack
  4000, Matrix 32 KiB, WhatsApp 60000) instead of being rejected.
- OIDC requires `exp` and `iss`; unknown-kid JWKS refreshes are
  rate-limited; a missing audience logs a startup warning.
- Audit egress is hash-chained by default with a generated HMAC key
  (`$XDG_STATE_HOME/rousseau/audit-chain.key`).
- `/readyz` on the metrics listener follows the transport link state;
  Helm readiness uses it; the full image has a `HEALTHCHECK`.
- New metrics: `rousseau_license_valid`,
  `rousseau_license_expires_timestamp_seconds`,
  `rousseau_audit_egress_*`. Spans for `agent.turn`,
  `provider.complete` and `agent.tool`; trace context propagates to
  A2A peers.
- MCP client stdin writes are serialised; WhatsApp joins in-flight
  work on shutdown; A2A tasks are evicted after `TaskRetention`;
  transport response bodies are bounded.
- nsjail backend: corrected flag spellings, minimal read-only root,
  `--keep_env`; smoke tests run when the binary is present.
- Email: `email.require_authentication_results` drops mail without a
  `dkim=pass` for the From domain (off by default, warns when off).

Ships in `v0.0.2` alongside the roadmap Wave 1-3 delivery.

### Added — Wave 1 (unblock credibility)

- **MCP client** (`internal/mcp/client`) — consume tools from
  external MCP servers (github, playwright, postgres, filesystem, …)
  by declaring them under `mcp.clients` in `config.yaml`. Stdio
  transport shipped; SSE / HTTP follow-ups.
- **spawn_subagent tool** — registers the existing
  `subagent.Spawn` primitive as a model-callable tool with a
  structured JSON summary output.
- **Container-release workflow** (`.github/workflows/container-
  release.yml`) — publishes both `full` and `distroless` images to
  ghcr.io on tag, cosign-signed with SLSA build attestations.
- **Benchmark harness** (`test/benchmarks/`) — SWE-Bench Verified,
  Aider Polyglot, and Terminal-Bench runners behind `//go:build
  bench`; weekly CI workflow uploads JSON artefacts.
- **`docs/compatibility.md`** — pkg/ vs internal/ stability
  contract; version-bump-neutral (per project convention every
  release increments by +0.0.1).

### Added — Wave 2 (Hermes-parity)

- **Voice-note transcription** (`internal/media/audio`) — Whisper.cpp
  local backend + OpenAI Whisper API fallback + Noop for tests.
  Wired end-to-end into WhatsApp (baseline), Telegram, and Discord —
  audio-only messages route through the transcriber and are delivered
  to the handler as normal text. Signal / iMessage / Matrix carry the
  same `Transcriber` config surface (uniform operator knob) with
  per-protocol audio-detection landing in v0.0.3.
- **Identity resolver** (`internal/identity` +
  `internal/state/sqlite/identity.go`) — maps
  `<transport>:<sender>` pairs to a stable identity so a
  conversation can span WhatsApp → Slack → email. Chat commands
  (`/whoami`, `/link`) in a follow-up.
- **Per-session cost telemetry** (`internal/pricing` +
  `internal/state/sqlite/session_costs.go` + `internal/state/sqlite/
  cost_recorder.go`) — records every completion's usage + estimated
  USD cost; `rousseau session cost [session-id]` CLI (with
  `--group-by`, `--since`, `--json`).
- **Prompt-cache instrumentation** — Anthropic adapter now sets
  1-hour TTL on system+tools and 5-minute TTL on messages (matches
  2026 caching guidance); `rousseau_prompt_cache_tokens_total`
  Prometheus counter split by type + TTL bucket for hit-ratio
  charts.

### Added — Wave 3 (differentiators)

- **Multi-model routing** (`internal/llm/router`) — new
  `provider: router` selects a child provider per request based on
  configurable rules (message length, tool-use count, session-id
  prefix). Emits `rousseau_router_decisions_total` metric.
- **Lifecycle hooks** (`internal/agent/hooks`) — external scripts
  fire at `pre_tool_use` / `post_tool_use` / `pre_turn` /
  `post_turn` / `on_error`. Deny verdict blocks the operation with
  a synthetic error surfaced to the model. Fail-open on hook errors.
- **Signed skills** (`internal/skills/verify.go`) —
  `SSHKeygenVerifier` verifies skill files via `ssh-keygen -Y
  verify` against an OpenSSH allowed-signers file. Strict mode
  drops unsigned skills; non-strict logs a WARN.
- **Bundled skills** (`skills/`) — starter set (`git-rebase`,
  `review-diff`, `whatsapp-transcript-summary`, `podman-quadlet`)
  copied into the container image at `/etc/rousseau/skills/`;
  user-drops under `~/.local/share/rousseau/skills/` still win.
- **Sandbox scaffold** (`internal/tools/sandbox` +
  `docs/security/sandbox.md`) — `none` backend fully shipped;
  `gvisor` / `nsjail` scaffolds wire the argv but need runtime
  binaries on PATH; `firecracker` scaffold-only.
- **A2A protocol runtime** (`internal/a2a`, `internal/a2a/server`,
  `internal/a2a/client`, `docs/a2a.md`, `examples/embed-a2a`) —
  HTTP/JSON server with SSE task streaming
  (`GET /.well-known/agent-capabilities`, `POST /tasks`,
  `GET /tasks/{id}`, `GET /tasks/{id}/events`,
  `POST /tasks/{id}/cancel`), bearer-token auth allowlist, in-memory
  task store with bounded history-replay so late SSE subscribers
  don't lose events, plus a matching client that `SubmitTask` →
  drains updates on a channel until terminal Status.

### Added — Wave 4 (moonshot scaffolds)

- **Letta-style memory runtime** (`internal/memory/letta` +
  `docs/memory-letta.md`) — in-memory `Store` (`NewMemoryStore`)
  implementing byte-budgeted core memory with auto-demotion of the
  oldest facts into substring-ranked archival memory on `WriteCore`.
  Persistent SQLite/vector backend still deferred (`NewSQLiteStore`
  returns `ErrScaffold`).
- **Plan-mode runtime** (`internal/agent/plan` +
  `docs/plan-mode.md`) — `Executor.Run` / `Rewind(n)` / `Resume`
  driving a Plan step-by-step with per-step approval gates and
  checkpoint recording. `MemoryCheckpointStore` ships as the
  default backend; SQLite persistence + the `/plan` chat command
  are follow-ups.
- **Workspace resolver runtime** (originally `internal/tenant` +
  `docs/multi-tenant.md`; renamed in v0.0.3 to `internal/workspace` +
  `docs/workspaces.md` — see ROADMAP §2.5) — `NewMapResolver([]Config)` →
  `Registry` with three allowlist patterns (exact
  `<transport>:<sender>`, transport-agnostic `<sender>`, catch-all
  `*`) + `ConfigFor(id)` / `All()` accessors for downstream
  per-workspace credentials + approver rules.

### Coverage

- `pkg/*` façade packages moved from 0% test coverage to 91.7%.
- Every new package ships with race-clean, `vet`-clean tests.

### Fixes

- `slsa.yml` example-verification comment referenced `v0.1.0` (a
  version that only comes after v0.0.999 per the project's
  monotonic +0.0.1 policy); corrected to `v0.0.1`.
- **Quadlet restart policy** (`docker/rousseau-agent.container`) —
  `Restart=on-failure` → `Restart=always`. whatsmeow exits cleanly
  (`status=0`) when its WhatsApp session is displaced
  (`StreamReplaced`, stream-end frame), which `on-failure` doesn't
  match, so a displacement left the bridge dead until manual
  restart. Now auto-recovers in ~10s. Rate-limited by
  `StartLimitBurst=5` / `StartLimitIntervalSec=600` so a genuinely
  broken image surfaces as `failed` in `systemctl status` rather
  than spinning silently.

### Licensing

- **Core relicensed to
  [FSL-1.1-Apache-2.0](https://fsl.software/FSL-1.1-Apache-2.0.template.md)**
  (Functional Source License, Version 1.1, Apache 2.0 Future
  License). Each version automatically transitions to Apache 2.0
  on the second anniversary of its release — the same fair-source
  pattern Sentry adopted. Rationale: block the hyperscaler-fork
  attack that forced HashiCorp / Elastic / MongoDB / Sentry to
  relicense under commercial pressure. Migration executed at
  ~1 GitHub star, when the cost is negligible.

  Versions v0.0.3 and earlier remain under the original Apache-2.0
  OR MIT dual license. `LICENSE-APACHE` and `LICENSE-MIT` are
  preserved in the tree as evidence of those historical terms.
  New versions ship under FSL-1.1-Apache-2.0 (see `LICENSE`). Full
  reasoning in `docs/LICENSE-RATIONALE.md`.
- **Contributor License Agreement adopted (Developer Certificate
  of Origin).** Every commit must carry a `Signed-off-by:` trailer
  (`git commit -s`). CI enforces via
  `.github/workflows/dco.yml`; the check emits paste-ready
  remediation commands on failure. Full text of the DCO
  attestation, corporate-contribution guidance, and rationale for
  DCO over a copyright-assignment CLA in `CLA.md`.

### Housekeeping

- `.gitignore` excludes `test/benchmarks/results/`.
- `docs/compatibility.md` documents the stability contract for
  `pkg/`, CLI flags, config schema, container tags, MCP protocol,
  providers, transports, metrics, and on-disk formats.
- **`make deploy`** — one-command update pipeline for the Quadlet
  daemon: rebuilds `localhost/rousseau-agent:local`, runs
  `systemctl --user restart rousseau-agent.service`, polls for
  `is-active`, then execs `rousseau version` inside the container
  to confirm the new binary is live. Replaces the previous
  three-step `make image && systemctl --user restart … && podman
  exec … version` incantation. Podman-only; guards on the unit
  being installed.

## [v0.0.1] — first tagged release

Marks the first cut of `rousseau-agent` with a versioned, attested
release artefact. Every capability listed here already existed on
`main`; this tag freezes it into a downloadable, verifiable bundle.

### Provider surface (LLM)

- Anthropic Messages API, with prompt-cache breakpoints on system
  prompt + tools (1-hour TTL) and last message (5-minute TTL)
- OpenAI Chat Completions (also drives OpenRouter and Ollama presets)
- Google Vertex AI with OAuth2 (service account or ADC)
- AWS Bedrock (Anthropic and Meta model families)
- Local `claude` CLI (`claudecli`) — inherits your Claude Code auth
  without plumbing API keys; opt-in `--bare` mode via
  `ROUSSEAU_CLAUDECLI_BARE=1` cuts cold-start when the mounted
  workspace is large

### Transports (chat channels)

- WhatsApp (via `whatsmeow`, incl. LID → PN self-chat substitution)
- Slack (Bolt + Socket Mode)
- Discord
- Telegram
- Signal (via `signal-cli`)
- Matrix (Synapse-compatible)
- SMS (Twilio, Vonage)
- iMessage (macOS-only)
- Email (IMAP + SMTP)

Each transport plugs into the same allowlist-first router; a single
daemon can run any single transport, and the same binary handles all
nine.

### Agent loop

- `internal/agent/agent.go` `Turn` orchestrates compression → recall
  system-prompt appendix → provider `Complete` → tool dispatch with
  approver gate → loop until end-of-turn
- Sub-agent primitive (`internal/agent/subagent`) available as a Go API
  (surfacing to the loop as a tool ships in a later release — see
  ROADMAP)
- Compression via `internal/agent/compressor` collapses long sessions
  while preserving credentials, TODOs, and cached prefixes
- Recall via SQLite FTS5 + hybrid vector search (`internal/recall`);
  configurable embedder (`voyage-ai` or the `noop` deterministic
  stub for tests)

### Tools

- Built-in filesystem set: `bash`, `read`, `write`, `edit`, `grep`
- Native integrations: GitHub, Slack, Google (Gmail/Calendar/Drive),
  Linear, Stripe
- Composio meta-integration adapter (registers every action the
  authenticated user has on Composio)

### Skills

- YAML-front-matter Markdown skills loader; skills with matched
  triggers get spliced into the system prompt as `## Skill Name`
  sections
- Skills directory resolves in this order: `--skills-dir` flag,
  `$ROUSSEAU_SKILLS_DIR`, `~/.local/share/rousseau/skills/`

### Scheduler

- `robfig/cron`-backed job runner with SQLite persistence
- Configurable poll interval (60 s default), delivery hook to any
  transport, Prometheus `rousseau_cron_fires_total{job,status}`

### OAuth broker

- Concurrent-flow-safe OAuth2 broker (`internal/auth/oauth/broker.go`)
- AEAD-encrypted token vault (SQLite-backed)
- Google, Linear, Composio flows

### MCP

- Server mode (`internal/mcp/server`) — rousseau exposes its tool
  registry as an MCP endpoint for external clients (Claude Desktop,
  Cursor, etc.)
- Client mode ships in v0.0.2 (see ROADMAP W1.3)

### Observability

- `log/slog`-only, dotted event names (`agent.turn`,
  `whatsapp.incoming`), redaction middleware for auth tokens
- Prometheus metrics: provider latency + errors, transport in/out,
  cron fires, tool calls, compressor rewrites, session gauge, panics
  recovered
- OpenTelemetry tracing via OTLP-HTTP (noop when no endpoint set)

### Supply-chain

- SLSA Level 3 provenance via `slsa-github-generator` reusable
  workflow — attests every artefact against a GitHub OIDC identity
- CycloneDX SBOM per archive (Syft-generated)
- Cosign-signed checksums file
- Reproducible bit-identical builds verified in CI
  (`reproducible-build.yml`)
- Cross-platform binaries in one release: Linux amd64/arm64/armv6/
  armv7/riscv64, macOS amd64/arm64, Windows amd64
- Two-flavour binaries: `rousseau` (full, all transports) and
  `rousseau-lite` (`-tags no_whatsmeow`, ~14 % smaller for operators
  who don't need WhatsApp)

### Container

- Two Dockerfile flavours:
  - `docker/Dockerfile` — full agent runtime with bash/git/python/
    node/mise/chezmoi for coding-tool use inside the container
  - `docker/Dockerfile.distroless` — minimal runtime for
    daemon-only deployments (WhatsApp bridge, cron scheduler, etc.)
- Rootless Podman with dropped capabilities, read-only rootfs,
  seccomp profile, UID/GID keep-id

### Quality gates

- 83.5 % test coverage across 159 test files, race detector on Linux
  and macOS
- `golangci-lint` v2 with 18 linters, `forbidigo` blocks `fmt.Print*`
  outside `main` and tests
- Fuzz tests on MCP protocol parsing and every transport that reads
  external bytes (WhatsApp, Discord, Email, Slack)
- `govulncheck` blocking on CVEs reaching imported symbols

## Verifying a release

```bash
# Grab the release + attestation
gh release download v0.0.1 -R sebastienrousseau/rousseau-agent \
    -p 'rousseau_v0.0.1_linux_amd64.tar.gz' \
    -p 'checksums.txt' \
    -p 'checksums.txt.sig' \
    -p 'multiple.intoto.jsonl'

# Verify the SLSA provenance (source-tag must match the release tag)
slsa-verifier verify-artifact \
    --provenance-path multiple.intoto.jsonl \
    --source-uri github.com/sebastienrousseau/rousseau-agent \
    --source-tag v0.0.1 \
    rousseau_v0.0.1_linux_amd64.tar.gz

# Verify cosign signature on the checksums
cosign verify-blob \
    --certificate-identity-regexp 'https://github.com/sebastienrousseau/rousseau-agent/.*' \
    --certificate-oidc-issuer https://token.actions.githubusercontent.com \
    --signature checksums.txt.sig \
    checksums.txt
```

[Unreleased]: https://github.com/sebastienrousseau/rousseau-agent/compare/v0.0.1...HEAD
[v0.0.1]: https://github.com/sebastienrousseau/rousseau-agent/releases/tag/v0.0.1
