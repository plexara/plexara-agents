# ADR 0001 — Use the `tool` directive in `go.mod` instead of the `//go:build tools` pattern

- Status: accepted
- Date: 2026-05-05
- Deciders: maintainers
- Supersedes: N/A
- Superseded by: N/A

## Context

The bootstrap specification (issue #1, §16.2) calls for pinning developer tools through the established `//go:build tools` convention: a `tools.go` file that blank-imports tool packages so `go.mod` records their versions.

That convention was the standard answer for years, but it has known drawbacks:

- Tool dependencies pollute the module's regular `require` graph, dragging in deep transitive trees that the runtime never uses.
- `go install <path>` (without a `@version` suffix) does not strictly enforce the pinned version unless the user is invoking from inside the module.
- The pattern is documented as a workaround, not a feature.

Go 1.24 (released February 2025) introduced a first-class `tool` directive in `go.mod` and a `go tool <name>` runner. Tool entries are tracked separately from the runtime require graph, version pinning is enforced at invocation time, and there is no `go install` step.

This project targets Go 1.26.

## Decision

Pin developer tools through the `tool` directive in `go.mod`. Invoke them through `go tool <name>`. Do not maintain a `tools.go` file or a separate `make tools` install step.

`go.mod` carries:

```
tool (
    github.com/golangci/golangci-lint/v2/cmd/golangci-lint
    github.com/google/go-licenses/v2
    github.com/securego/gosec/v2/cmd/gosec
    golang.org/x/tools/cmd/goimports
    golang.org/x/vuln/cmd/govulncheck
)
```

The Makefile wraps these. CI calls them through the same wrapper, so local and CI invocations are identical.

Release-time tools that are *not* Go modules in any meaningful sense — Cosign and Syft, called by GoReleaser — are pinned in the release workflow and GoReleaser configuration in phase 2 (issue #3), not in `go.mod`. Pulling them into the module graph through the legacy tools pattern previously expanded `go.mod` to 657 lines and `go.sum` to 2637 lines without runtime benefit.

## Consequences

**Positive**

- `go.mod` is roughly 60% shorter and `go.sum` roughly 60% shorter than under the legacy pattern.
- Tool versions are enforced at every invocation; running `go tool golangci-lint run` always uses the version pinned in `go.mod`, regardless of `$PATH`.
- Contributors do not need to manage a separate tool toolchain. `go mod tidy` is sufficient setup.
- The `tool` directive is the language-level mechanism for this problem; the project moves with the ecosystem rather than against it.

**Negative**

- Requires Go 1.24 or newer. The project requires Go 1.26 (see `go.mod`), so this is a non-issue in practice.
- Editor and IDE integrations sometimes still expect tools on `$PATH`. Where that matters, a developer can run `go install <path>@<version>` themselves; the version is recoverable from `go.mod`.
- This decision deviates from the bootstrap spec (issue #1, §16.2) as written. The spec was drafted before the Go team finalized the `tool` directive's behavior, and the maintainers prefer the modern mechanism.

**Cosign and Syft are out of the module graph.** They are pinned in the release workflow (phase 2). Reproducible-build verification documentation (`SECURITY.md`) will reference those pinned versions once the release pipeline lands.

## Alternatives considered

1. **Keep the `//go:build tools` pattern, as the spec calls for.** Rejected: produces a 657-line `go.mod` for zero runtime gain, and the language has moved on.
2. **Use a separate dev-tools repository.** Rejected: out of proportion for the size of this project; adds a coordination burden that the `tool` directive obviates.
3. **Pin tools only in CI, leave local development unpinned.** Rejected: drift between CI and local environments is exactly what tool pinning exists to prevent.

## References

- Go 1.24 release notes — `tool` directive: <https://go.dev/doc/go1.24#go-command>
- Go modules reference — tool dependencies: <https://go.dev/ref/mod#go-mod-file-tool>
- Issue #1, §16.2 — original bootstrap specification (legacy `//go:build tools` pattern).
