# Contributing

Private project. Contributions accepted from invited collaborators only.

## Development

```bash
git clone https://github.com/sebastienrousseau/rousseau-agent
cd rousseau-agent
make setup   # installs golangci-lint, govulncheck
make check   # vet + lint + test + govulncheck
```

## Commit style

Conventional Commits (`feat:`, `fix:`, `refactor:`, `docs:`, `test:`, `chore:`). Keep subjects under 72 characters. Body explains *why*, not *what*.

## Code standards

- Every exported identifier has a godoc comment.
- No `interface{}` / `any` in public APIs unless there is a written justification.
- Contexts propagate through every I/O path. No hidden globals.
- Errors wrap with `fmt.Errorf("...: %w", err)`; sentinel errors go in the package's `errors.go`.
- No panics outside `main` and test helpers.

## Testing

- Unit tests live next to the code (`foo.go` → `foo_test.go`).
- Integration tests live in `test/integration/`.
- Table-driven tests preferred. Use `testify/require` for stopping-condition assertions and `testify/assert` for non-blocking ones.

## PRs

Every PR must include a rationale linking to the underlying decision or ticket. Green CI is not sufficient — reviewer approval is required.
