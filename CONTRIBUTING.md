# Contributing to rousseau-agent

Private project. Contributions accepted from invited collaborators only. This document sets the standards every change is held to.

## Getting a development environment

```bash
git clone https://github.com/sebastienrousseau/rousseau-agent
cd rousseau-agent
make setup      # installs golangci-lint (v2) and govulncheck
make check      # vet + lint + race-tests + govulncheck
```

Every check that runs in CI is available locally through the Makefile. If a change passes `make check`, it will pass CI.

## Commit standards

- Follow [Conventional Commits](https://www.conventionalcommits.org/): `feat:`, `fix:`, `refactor:`, `docs:`, `test:`, `chore:`, `ci:`, `perf:`.
- Subject line ≤ 72 characters. Body explains **why**, not what. Reference the driving decision, issue, or incident.
- Do not amend published commits. Create a new commit; the reviewer prefers a series they can bisect.
- Sign your commits if you have signing configured. The project does not currently require signatures, but they are recommended for release-tag commits.

## Code standards

- Every exported identifier has a godoc comment beginning with the identifier name.
- No `interface{}` / `any` in exported APIs without a written justification in the doc comment.
- `context.Context` propagates through every I/O path. No hidden globals or ambient loggers; pass `*slog.Logger` explicitly.
- Errors wrap upward with `fmt.Errorf("...: %w", err)`. Sentinel errors go in the package's `errors.go`. Prefer `errors.Is` / `errors.As` at call sites over string matching.
- No panics outside `main` and test helpers. `Must*` variants that panic on operator error (duplicate registration, invalid static schema) are allowed with a documented rationale.
- No `fmt.Print*` in library code. Use `slog` or a TUI model. The `forbidigo` linter enforces this.

## Test standards

- Unit tests live next to the code: `foo.go` → `foo_test.go`.
- Table-driven tests preferred. Use `require` for stopping assertions, `assert` for non-stopping ones.
- Interface-based test injection is preferred over global patching. Every transport package defines a narrow interface (`WSConn`, `IMAPClient`, `HTTPClient`, `Sender`) that tests satisfy with fakes.
- Coverage target: 85 % for pure business-logic packages; 75 % overall (WebSocket dial paths and subprocess-management code are legitimately hard to test without dedicated fakes).
- Race-safe: `go test -race` must pass. New concurrent code needs a race test if it introduces non-trivial synchronisation.
- Fuzz functions are added for every parser (`FuzzParseFoo` next to `parseFoo`). `make fuzz` runs the corpus.

## Pull request process

1. Open the PR against `main`. Rebase (do not merge) if `main` moves under you.
2. Every PR requires:
   - A rationale in the description (2–3 sentences linking to the underlying decision).
   - Green CI: `vet`, `lint`, `test-race` on Linux + macOS, `govulncheck`, `codeql`, `reproducible-build`, coverage floor.
   - Reviewer approval. Green CI is necessary but not sufficient.
3. Squash merges only. The merge commit message is the final commit message and lands on `main` as one atomic change.
4. If the PR adds a new dependency, note the justification in the description. Prefer standard library over adding a dependency; prefer an existing dependency over adding a new one.

## Reviewer checklist

Reviewers verify, in order:

1. **Necessity.** Is the change required, or does it add abstraction / feature surface without a driving requirement?
2. **Scope.** Does the change stay within its stated purpose, or does it bundle unrelated cleanups?
3. **Boundary integrity.** Does the change respect the `agent → concrete` dependency direction?
4. **Test coverage.** Are new code paths covered? Are edge cases exercised?
5. **Error handling.** Are errors wrapped with context? Are cleanup paths honest (`_ =` with a `//nolint:errcheck` justification, not silently swallowed)?
6. **Godoc + linter clean.** Every exported symbol documented; lint output is 0 issues.
7. **Security.** Does the change touch the `bash` tool, approval policy, transport auth, or container posture? If yes, does the PR description flag it?

## Release process

Releases are cut from `main`:

1. Update `CHANGELOG` entries.
2. Tag as `vX.Y.Z` on the release commit.
3. The `release` workflow builds via GoReleaser, generates a CycloneDX SBOM, publishes a cosign signature of the checksums, and generates SLSA-3 provenance.
4. Consumers verify per the instructions in [SECURITY.md](./SECURITY.md).

## Governance

`rousseau-agent` is a single-maintainer project at present. Decision authority rests with the maintainer of record listed in `go.mod` and the `LICENSE`. Contributors are welcome to propose direction changes via PR discussion or by email.
