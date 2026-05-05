# plexara-agents

A Go reference implementation for building MCP-driven AI agents.

[![License](https://img.shields.io/github/license/plexara/plexara-agents)](LICENSE)

> Status: pre-v0.1, under active bootstrap. The API will change without notice until `v0.1.0` is tagged.
>
> CI, coverage, Go Report Card, OpenSSF Scorecard, release, and pkg.go.dev badges land as their underlying integrations come online in phases 2 (CI), 3+ (coverage / Go reference), and at first release (release / Scorecard).

## What this is

`plexara-agents` ships two things:

1. **`core/`** — a small, opinionated Go library for building local-first AI agents that drive Model Context Protocol (MCP) servers.
2. **`cmd/`** — ready-to-run binaries (`ask`, `repl`) that use `core` to demonstrate the Plexara MCP data platform against the public ACME Corp demo deployment.

The project is designed to be read end-to-end. It runs entirely on local model inference (Ollama or MLX) and treats MCP as a first-class primitive. Plexara is the showcase, not a coupling — `core` drives any MCP server.

## Status

This repository is being bootstrapped. See the [project bootstrap issue](https://github.com/plexara/plexara-agents/issues/1) for the v0.1 specification and the linked phase tickets for active work.

## Requirements

- Go 1.26 or newer
- A local OpenAI-compatible inference runtime (Ollama or `mlx-lm` server)
- An MCP server to drive (the Plexara ACME demo at `https://mcp-demo.plexara.io` is the headline target)

## License

Apache 2.0 — see [LICENSE](LICENSE).

## Security

See [SECURITY.md](SECURITY.md) for vulnerability reporting and the supported-versions matrix.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). All contributors are expected to follow the [Code of Conduct](CODE_OF_CONDUCT.md).
