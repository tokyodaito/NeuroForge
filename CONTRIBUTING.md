# Contributing to NeuroForge

Thanks for contributing — human or agent. This guide complements
[`AGENTS.md`](AGENTS.md) (the operational rulebook) and
[`docs/spec/NEUROFORGE_SPEC.md`](docs/spec/NEUROFORGE_SPEC.md) (the requirements).
When in doubt, **the spec wins**, and an architectural deviation needs an ADR.

## Prerequisites

- Go ≥ 1.23 (developed on Go 1.26).
- Git.
- (Optional) `golangci-lint` — `make lint` falls back to `go vet` if absent.

If Go is missing on a machine, do **not** silently install it. Record the
requirement and follow an explicit install (`brew install go` or
https://go.dev/dl/).

## Get started

```sh
git clone <repo> neuroforge
cd neuroforge
make build      # ./forge
make check      # fmt-check + go vet + tests
./forge version
```

## Development loop

1. Pick an issue from
   [`docs/milestones/IMPLEMENTATION_PLAN.md`](docs/milestones/IMPLEMENTATION_PLAN.md).
   Respect its **allowed paths** and **forbidden paths**.
2. Branch from `main`: `feat/<scope>` or `fix/<scope>`.
3. Keep packages within their boundaries (see AGENTS.md table). Do not put
   adapter code in core packages or vice-versa.
4. Write code + tests together. Every acceptance criterion (AC-1 … AC-30) needs an
   automated or integration test (rule §36.22).
5. Format and verify before finishing:

   ```sh
   make fmt         # gofmt -w .
   make check       # fmt-check + vet + tests
   ```

6. If you deviate from the spec or an ADR, add or update an ADR in `docs/adr/`.

## Code style

- `gofmt` is the source of truth; `.editorconfig` documents it for editors.
- Tabs for `.go`/`Makefile`; two spaces for YAML/Markdown.
- No commented-out code, no fake stubs (rule §36.25). If something is not yet
  implemented, mark it explicitly (e.g. the scaffold packages' `doc.go` "STATUS:
  not implemented" markers, or a clear `// not implemented: see M<n>` in the
  compliance matrix).
- Keep external dependencies at zero unless justified (AGENTS.md).

## Tests

- Unit tests live next to the code (`*_test.go`, same package unless white-box is
  required).
- Use table-driven tests where it aids clarity (see `internal/cli/cli_test.go`).
- No real paid models in CI — use the fake agents/providers that arrive in M2/M9.
- Integration scenarios are enumerated in the spec §33.4; each maps to an issue.

## Commits

- Conventional subjects: `feat:`, `fix:`, `docs:`, `test:`, `chore:`, `refactor:`.
- Imperative mood; body wrapped at ~72 chars.
- Reference the issue/AC, e.g. `feat(cli): add version command (M0-2, AC-n/a)`.
- Small, focused commits; never mix unrelated changes.
- Never commit secrets, `.neuroforge/`, or the built `forge` binary.

## Pull requests

- The PR description must include: what changed, why, which AC/issue it addresses,
  and (if any) why a new dependency was added.
- `make check` must be green in the PR.
- For architectural changes, link the ADR.

## Review checklist (for reviewers)

- [ ] `make check` is green.
- [ ] Change stays inside its package boundaries.
- [ ] No new external dependency, or it is fully justified.
- [ ] Unimplemented behaviour is explicitly marked, not faked.
- [ ] Tests cover the relevant acceptance criterion.
- [ ] Docs/ADRs/COMPLIANCE_MATRIX updated where applicable.
