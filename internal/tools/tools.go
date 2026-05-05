//go:build tools

// Package tools tracks dev-only tooling so `go install` produces a
// reproducible local toolchain. None of these imports are referenced
// at runtime; they exist only to pin versions in go.mod.
//
// Run `make tools` to install everything in this list.
package tools

import (
	_ "github.com/anchore/syft/cmd/syft"
	_ "github.com/golangci/golangci-lint/v2/cmd/golangci-lint"
	_ "github.com/google/go-licenses"
	_ "github.com/securego/gosec/v2/cmd/gosec"
	_ "github.com/sigstore/cosign/v2/cmd/cosign"
	_ "golang.org/x/tools/cmd/goimports"
	_ "golang.org/x/vuln/cmd/govulncheck"
)
