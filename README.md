# plexara-agents

A Go reference implementation for building MCP-driven AI agents.

[![CI](https://github.com/plexara/plexara-agents/actions/workflows/ci.yml/badge.svg)](https://github.com/plexara/plexara-agents/actions/workflows/ci.yml)
[![Coverage](https://codecov.io/gh/plexara/plexara-agents/branch/main/graph/badge.svg)](https://codecov.io/gh/plexara/plexara-agents)
[![Go Report Card](https://goreportcard.com/badge/github.com/plexara/plexara-agents)](https://goreportcard.com/report/github.com/plexara/plexara-agents)
[![OpenSSF Scorecard](https://api.scorecard.dev/projects/github.com/plexara/plexara-agents/badge)](https://scorecard.dev/viewer/?uri=github.com/plexara/plexara-agents)
[![Release](https://img.shields.io/github/v/release/plexara/plexara-agents)](https://github.com/plexara/plexara-agents/releases)
[![License](https://img.shields.io/github/license/plexara/plexara-agents)](LICENSE)
[![Go Reference](https://pkg.go.dev/badge/github.com/plexara/plexara-agents.svg)](https://pkg.go.dev/github.com/plexara/plexara-agents)

> Status: pre-v0.1, under active bootstrap. The API will change without notice until `v0.1.0` is tagged.

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
