# Security Policy

We take the security of `plexara-agents` seriously. This document explains how to report vulnerabilities, what we support, and how we ship and verify releases.

## Reporting a vulnerability

**Do not open a public GitHub issue for security vulnerabilities.**

Use one of the following private channels:

1. **GitHub Private Vulnerability Reporting** (preferred): <https://github.com/plexara/plexara-agents/security/advisories/new>
2. **Email:** `support@plexara.io` with subject line beginning `[SECURITY]`

Please include:

- A description of the vulnerability and its impact.
- The version, commit SHA, or release tag affected.
- Steps to reproduce, or a proof-of-concept.
- Any suggested mitigations, if you have them.

We will acknowledge receipt within **3 business days**, provide an initial assessment within **7 business days**, and aim to ship a fix within **30 days** for high-severity issues. We will coordinate disclosure with you.

## Supported versions

> No tagged releases exist yet. This matrix describes the policy that takes effect once `v0.1.0` ships. Until then, only the `main` branch is supported, and security fixes will be merged directly to `main`.

Once releases exist:

| Version  | Supported |
| -------- | --------- |
| Latest `0.x` minor | yes (security fixes merged to `main` and a patch release cut) |
| Older `0.x` minors | no — please upgrade |
| `< 0.1`  | n/a (pre-release) |

After `v1.0.0`, the policy will be revisited and a longer support window introduced.

## Verifying releases

Every release artifact is signed with [Sigstore Cosign](https://docs.sigstore.dev/) using GitHub OIDC. SBOMs (CycloneDX and SPDX) and SLSA Level 3 provenance are attached to every GitHub release.

Verification commands will be documented here once the first signed release is published.

## Scope

Security issues we will accept:

- Vulnerabilities in `core/`, `cmd/`, or any code published from this repository.
- Vulnerabilities in our build, release, or supply-chain pipeline.
- Insecure defaults that could put users at risk.

Out of scope:

- Vulnerabilities in upstream dependencies — please report those directly to the upstream project. We will track them via `govulncheck` and Dependabot.
- Issues in MCP servers driven by this client — report those to the MCP server's maintainers.
- Vulnerabilities in local inference runtimes (Ollama, mlx-lm, etc.).

## Hall of fame

We will credit reporters who request public acknowledgement once a fix is released.
