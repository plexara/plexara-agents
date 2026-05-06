# Contributing

Thanks for your interest. This document covers local development setup, the workflow we use for changes, and the standards every PR is expected to meet.

By participating in this project you agree to abide by the [Code of Conduct](CODE_OF_CONDUCT.md).

## Local development

### Prerequisites

- Go 1.26 or newer
- `make`
- A local OpenAI-compatible inference runtime if you plan to run examples or integration tests:
  - [Ollama](https://ollama.com), or
  - [`mlx-lm`'s OpenAI-compatible server](https://github.com/ml-explore/mlx-examples) on Apple Silicon

### One-time setup

```sh
git clone https://github.com/plexara/plexara-agents.git
cd plexara-agents
go mod tidy   # materializes the dev tools pinned via the `tool` directive in go.mod
```

Tools are pinned via Go's `tool` directive (Go 1.24+). They are invoked through `go tool <name>` (e.g. `go tool golangci-lint run`). The Makefile wraps these. There is no separate `go install` step.

Optional but recommended:

```sh
pip install pre-commit
pre-commit install
```

### Common tasks

```sh
make verify   # run every check CI runs that can run locally — REQUIRED before pushing
make build    # go build ./...
make test     # go test -race -shuffle=on -count=1 -covermode=atomic ./...
make lint     # golangci-lint run
make sec      # gosec + govulncheck
make cover    # produce coverage.out and a human-readable summary
make tidy     # go mod tidy
make help     # full target list
```

### Before every commit: the Pre-commit Review Loop (PRL)

Code is not done until an independent review pass returns **zero blockers and zero should-fixes**. The loop:

1. Write code. Add or update tests. Run `make verify`.
2. Dispatch a critical-review agent (or arrange for a human reviewer) with a depth-of-scrutiny brief.
3. Fix every blocker and should-fix the review surfaces. Address every design question with a code change or an explicit rationale.
4. Re-run `make verify`.
5. Dispatch a *second* review pass to confirm the fixes are clean and no new issues were introduced.
6. Iterate until review is clean.
7. Only then commit. The commit message must include a `Reviewed-by:` trailer.

PRL applies to commits whose Conventional Commits type is `feat`, `fix`, `refactor`, `perf`, or `build`. It does not apply to pure `docs` / `chore` / `style` / `test` commits.

The full protocol lives at `docs/review-protocol.md`. Do not skip it; the review-after-commit pattern wastes far more time than it saves.

### Before every push: `make verify`

`make verify` is the canonical "ready to push" gate. It runs every check the CI pipeline runs that can run locally:

- `gofmt -l`, `go mod verify`, `go mod tidy -diff`
- `go vet`, `golangci-lint config verify`, `golangci-lint run`
- `go test -race -shuffle=on -count=1 -covermode=atomic`
- Cross-compile build matrix (`darwin/{amd64,arm64}`, `linux/{amd64,arm64}`, `windows/amd64`)
- `gosec`, `govulncheck`
- **Semgrep** in the same Docker image CI uses (skipped with a warning if Docker isn't available locally — but if Docker is available and you don't run it, you cannot prove a clean run)
- 5s fuzz pass per `Fuzz*` target

**If `make verify` fails, do not push.** Fix the failure, re-run until green.

### Local hooks (recommended)

Enable the repo's hooks once per clone so PRL and `make verify` run automatically:

```sh
git config core.hooksPath .githooks
```

This installs three hooks:

- `pre-commit` — prints the PRL checklist before each commit.
- `commit-msg` — refuses `feat` / `fix` / `refactor` / `perf` / `build` commits that lack a `Reviewed-by:` trailer.
- `pre-push` — runs `make verify` before each push.

For genuine emergencies:

- `SKIP_REVIEW=1 git commit ...` bypasses the PRL gate
- `SKIP_VERIFY=1 git push` bypasses the verify gate

Don't abuse them; if you bypass, explain why in the commit body or PR description.

## Workflow

1. **Find or open an issue.** All non-trivial changes start as an issue. Phase tickets (see #1) are the canonical work units for the v0.1 bootstrap.
2. **Branch from `main`.** Branch names follow `feat/<short-name>`, `fix/<short-name>`, `docs/<short-name>`, `chore/<short-name>`.
3. **Write tests.** New code under `core/...` is expected to keep coverage above 80%. Replay tests live under `testdata/sessions/`; new examples must ship with at least one.
4. **Open a PR against `main`.** Fill in the PR template. Link the issue.
5. **Review and merge.** PRs require at least one approving review and a green CI run. The branch is squashed or rebased on merge — no merge commits.

### Commit messages

We use [Conventional Commits](https://www.conventionalcommits.org). PR titles are checked in CI. Examples:

```
feat(core/loop): add MaxSteps cap to agent loop
fix(core/provider): buffer tool-call deltas until finish_reason
docs(adrs): add ADR-0001 for provider model choice
chore(deps): bump golang.org/x/sync to v0.10.0
```

Allowed types: `feat`, `fix`, `docs`, `chore`, `refactor`, `test`, `perf`, `ci`, `build`, `revert`.

### Signed commits

Commits to `main` are required to be signed (GPG, SSH, or S/MIME). For SSH signing — the lowest-friction path on macOS and most Linux setups:

```sh
git config --global commit.gpgsign true
git config --global gpg.format ssh
git config --global user.signingkey ~/.ssh/id_ed25519.pub
git config --global gpg.ssh.allowedSignersFile ~/.ssh/allowed_signers
```

Then add your signing key to GitHub: <https://github.com/settings/ssh/new> with the key type set to "Signing Key" (separate from authentication keys). Without that step, GitHub will accept the commit but show the signature as unverified.

For GPG, replace the `gpg.format` and `user.signingkey` lines with your GPG key ID. See <https://docs.github.com/en/authentication/managing-commit-signature-verification> for end-to-end setup.

## Code standards

The full set is enforced by `golangci-lint` (see `.golangci.yml`) and CI. The high points:

- `gofmt` and `goimports` clean. CI fails on unformatted files.
- `go vet`, `staticcheck`, `errcheck`, `gosec`, `govulncheck` clean.
- Public APIs documented with full sentences. Private functions documented when non-obvious.
- Interfaces defined at the consumer, not the producer.
- `context.Context` is the first parameter of any function that does I/O.
- No package-level mutable state.
- Errors wrap with `fmt.Errorf("%w", ...)`. Sentinel errors live next to the package that owns them.
- No vendoring. Module graph resolved through the proxy and verified via `go.sum` and `go mod verify`.

## Reporting security issues

Please do not open public issues for security vulnerabilities. See [SECURITY.md](SECURITY.md) for the disclosure process.
