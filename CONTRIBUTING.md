# Contributing to NewPress

Maintainer: **ChunYang**. Main repository: https://github.com/chunyangnet/NewPress; domestic mirror: https://gitee.com/chunyangnet_0/newpress. General contact: [Chunnel@linux.do](mailto:Chunnel@linux.do). Keep security reports private as described in [SECURITY.md](SECURITY.md).

The main program and the independently hosted themes/plugins have different GitHub owners. Do not rewrite their repository URLs when updating main-program metadata.

## Before coding

Read [README.md](README.md), [DESIGN.md](DESIGN.md), the [theme development guide](themes/README.md) or [plugin development guide](plugins/README.md), and the relevant package code and tests. NewPress provides WordPress-like content semantics, not PHP plugin or theme compatibility. Propose large API, database, plugin ABI, theme contract or deployment changes in an issue before implementation, including compatibility and migration plans.

The local `docs/` directory is private and is not distributed. Public contribution instructions must be self-contained in the repository's README files, code examples and tests; do not require private documents. Never commit local credentials, runtime data or generated verification output.

## Development workflow

1. Create a focused branch and keep unrelated changes out.
2. Add tests for behavior and hostile input, not only the happy path.
3. Update public docs, manifest examples and migration notes.
4. Run formatting, tests, race detector, vet, vulnerability scan and build.
5. Describe performance, security, compatibility and rollback impact in the change request.

```powershell
gofmt -w (rg --files -g '*.go')
node --test tests/frontend/*.test.cjs
go test ./...
go test -race ./...
go vet ./...
go build -trimpath -buildvcs=false ./cmd/newpress
go run golang.org/x/vuln/cmd/govulncheck@latest ./...
```

## Code rules

- Prefer the standard library and small, maintained dependencies.
- Accept context at I/O boundaries and set finite deadlines.
- Do not expose database pools, raw config, secrets or Go pointers to plugins/templates.
- Never add an unauthenticated write route to the M0 console.
- Use parameterized SQL and bounded result sets.
- Do not edit a migration after it has shipped; add a new migration.
- Use `html/template` contextual escaping; only sanitizer output may become trusted HTML.
- Third-party server extensions default to WASM; native in-process marketplace code is not accepted.
- Preserve clean-room boundaries: no B2 or protected theme source/assets/licensing logic.

## Commit and review scope

Commits should explain intent. Reviews pay particular attention to package paths, capability checks, auth/object ownership, transaction boundaries, cache invalidation, migration rollback, resource limits and user-visible compatibility.

## Licenses

By contributing, you agree that your contribution may be distributed under the repository license and that you have the right to submit it. Do not add copied commercial theme assets or dependencies with incompatible obligations.
