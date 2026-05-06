# CI and Release Pipeline

Operator's runbook for the GitHub Actions workflows under `.github/workflows/` and the GoReleaser configuration at `.goreleaser.yaml`.

## Workflow inventory

| Workflow | Trigger | Purpose |
| --- | --- | --- |
| `ci.yml` | PR + push to `main` | Build matrix, lint, test, coverage upload to Codecov |
| `security.yml` | PR + push + weekly cron | gosec, govulncheck, Semgrep, Trivy fs scan |
| `codeql.yml` | PR + push + weekly cron | GitHub CodeQL with `security-extended` + `security-and-quality` |
| `scorecard.yml` | push to `main` + weekly cron + branch protection rule | OpenSSF Scorecard |
| `dependency-review.yml` | PR | Block PRs that introduce vulnerable or non-permissive deps |
| `pr-title.yml` | PR opened/edited/sync | Conventional Commits PR title check |
| `fuzz.yml` | nightly cron + manual | Extended fuzz cycles, one matrix entry per `Fuzz*` target |
| `release.yml` | tag `v*.*.*` | GoReleaser, cosign keyless signing, SBOMs, SLSA build provenance |

## Required and optional secrets

| Secret | Required for | Notes |
| --- | --- | --- |
| `GITHUB_TOKEN` | every workflow | Provided by GitHub automatically. No action needed. |
| `CODECOV_TOKEN` | `ci.yml` (coverage upload) | Required. Obtain from <https://app.codecov.io>. |
| `SCORECARD_READ_TOKEN` | `scorecard.yml` (Branch-Protection check only) | Optional. Without it, the Branch-Protection check returns `?` but every other check still runs. To populate: GitHub → Settings → Developer settings → Personal access tokens → Fine-grained → tokens scoped to this repo with **Administration: Read** + **Metadata: Read**. |
| `HOMEBREW_TAP_GITHUB_TOKEN` | `release.yml` (Homebrew formula publish) | Optional. Without it, GoReleaser's `brews` step skips upload (`skip_upload: auto`). To populate: fine-grained PAT scoped to `plexara/homebrew-tap` with **Contents: Write** + **Metadata: Read**. |

To inspect or update repository secrets, run:

```sh
gh secret list --repo plexara/plexara-agents
gh secret set CODECOV_TOKEN --repo plexara/plexara-agents
```

## Action pinning

Every third-party action is pinned to a full commit SHA with the human-readable version as a trailing comment, per spec §14.10. Example:

```yaml
- uses: actions/checkout@de0fac2e4500dabe0009e67214ff5f5447ce83dd # v6.0.2
```

Dependabot (`.github/dependabot.yml`) updates the SHAs weekly. **Reviewers must verify that the new SHA points at the same release the trailing comment claims.** OpenSSF Scorecard's `Pinned-Dependencies` check enforces this; missing pins fail Scorecard.

## Coverage gates

`.github/codecov.yml` configures:

- `core/` target: 80% statement coverage, 1pp threshold, `if_ci_failed: error`
- `cmd/`, `examples/`, `docs/`, test files, and mocks excluded from the gate

Per spec §14.3: `cmd/...` and `examples/...` are exercised in tests but not gated; they exist as worked examples and integration scaffolding.

## Empty source tree handling

Until phase 3 (#4) lands the first Go source file, every CI workflow detects this and skips Go-source-dependent steps with an explanatory log message. The `lint` job still runs `go mod verify`, `go mod tidy -diff`, and `golangci-lint config verify` even on the empty tree (these don't need Go source).

Steps that require Go source and are gated behind the detect job include: `gofmt`, `go vet`, `golangci-lint run`, `go test`, `go build`, CodeQL analyze, `gosec`, `semgrep`, and `govulncheck` (which exits 1 with "no packages matched" on an empty tree). Trivy scans the filesystem and runs unconditionally.

Once Go source lands, the detect step's `has-source` output flips to `true` and the rest of the pipeline activates without any workflow change.

**Branch protection note**: do not enable required status checks for source-dependent jobs (`govulncheck`, `gosec`, `semgrep`, CodeQL `analyze go`, `lint` if you've added stricter sub-steps) until *after* phase 3 lands. Skipped jobs do not satisfy a required status check. The `ci pass` aggregator and the always-running checks (`review`, `conventional commit`, `trivy fs`) are safe to require immediately.

## Release process

1. **Cut a tag.** Tags must follow strict SemVer: `vX.Y.Z` for releases, optional pre-release suffixes (`-alpha`, `-beta`, `-rc.N`).
   ```sh
   git tag -s v0.1.0 -m "v0.1.0"
   git push origin v0.1.0
   ```
2. **Tag push triggers `release.yml`**, which:
   - Runs GoReleaser with cross-compile matrix (`darwin/{amd64,arm64}`, `linux/{amd64,arm64}`, `windows/amd64`).
   - Generates CycloneDX and SPDX SBOMs per archive via Syft.
   - Signs the checksums file with Cosign keyless (Sigstore OIDC) — produces `_checksums.txt.sig` and `_checksums.txt.pem`.
   - Generates SLSA v1 build provenance for every archive and SBOM via `actions/attest-build-provenance`. Verifiable with `gh attestation verify`.
   - Optionally publishes a Homebrew formula to `plexara/homebrew-tap` if `HOMEBREW_TAP_GITHUB_TOKEN` is set.
3. **Verifying artifacts** (documented in `SECURITY.md`):
   ```sh
   # Cosign-verify the checksums file
   cosign verify-blob \
     --certificate-identity 'https://github.com/plexara/plexara-agents/.github/workflows/release.yml@refs/tags/v0.1.0' \
     --certificate-oidc-issuer 'https://token.actions.githubusercontent.com' \
     --certificate plexara-agents_v0.1.0_checksums.txt.pem \
     --signature   plexara-agents_v0.1.0_checksums.txt.sig \
     plexara-agents_v0.1.0_checksums.txt

   # Verify SLSA provenance
   gh attestation verify plexara-agents_v0.1.0_Linux_x86_64.tar.gz \
     --owner plexara
   ```

## Branch protection (manual setup)

Branch protection cannot be configured via these workflows. After the first green run on `main`, configure under GitHub → Settings → Branches → main:

- Require pull request before merging (1 approving review, dismiss stale approvals on new commits, require code-owner review on owned paths).
- Require status checks to pass before merging:
  - `ci pass`
  - `lint`
  - `analyze go` (CodeQL)
  - `govulncheck`
  - `gosec`
  - `semgrep`
  - `trivy fs`
  - `review` (dependency-review)
  - `conventional commit` (pr-title)
- Require linear history (squash or rebase, no merge commits).
- Require signed commits.
- Block force pushes.
- Allow Dependabot to auto-merge PRs that pass all checks.

## Troubleshooting

- **Codecov upload fails with `Token Required`**: confirm `CODECOV_TOKEN` is set on the repo (`gh secret list`).
- **Scorecard "Branch-Protection" returns `?`**: this is expected without `SCORECARD_READ_TOKEN`. Add the secret if you want full Scorecard coverage.
- **`pr-title` fails with "subject must start with alphanumeric"**: PR title is missing the conventional-commits subject (e.g. `feat(core/loop): ` with nothing after the colon). Fix the title.
- **Release fails on `brews` step**: `HOMEBREW_TAP_GITHUB_TOKEN` is unset and `skip_upload: auto` is supposed to skip. If failing anyway, inspect GoReleaser logs — most often the tap repo doesn't exist or the token lacks `Contents: Write` on it.
