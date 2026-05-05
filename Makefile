.PHONY: all build test lint sec cover tidy tools fmt vet vuln clean help

GO        ?= go
GOFLAGS   ?=
PKGS      ?= ./...
COVERPROFILE ?= coverage.out

# Pinned dev tools — installed by `make tools`. Versions tracked in internal/tools/tools.go.
GOLANGCI_LINT ?= golangci-lint
GOIMPORTS     ?= goimports
GOVULNCHECK   ?= govulncheck
GOSEC         ?= gosec

all: build lint test ## Run build, lint, and test.

build: ## Build all packages.
	$(GO) build $(GOFLAGS) $(PKGS)

test: ## Run tests with race detector, shuffle, and coverage.
	$(GO) test -race -shuffle=on -count=1 \
		-covermode=atomic -coverprofile=$(COVERPROFILE) \
		$(PKGS)

cover: test ## Show coverage summary.
	$(GO) tool cover -func=$(COVERPROFILE) | tail -n 1
	$(GO) tool cover -html=$(COVERPROFILE) -o coverage.html
	@echo "Wrote coverage.html"

lint: ## Run golangci-lint.
	$(GOLANGCI_LINT) run $(PKGS)

fmt: ## Format Go code.
	$(GOIMPORTS) -w -local github.com/plexara/plexara-agents .

vet: ## Run go vet.
	$(GO) vet $(PKGS)

sec: ## Run security scanners (gosec + govulncheck).
	$(GOSEC) -quiet $(PKGS)
	$(GOVULNCHECK) $(PKGS)

vuln: ## Run only govulncheck.
	$(GOVULNCHECK) $(PKGS)

tidy: ## Tidy go.mod and verify modules.
	$(GO) mod tidy
	$(GO) mod verify

tools: ## Install pinned dev tools.
	@echo "Installing dev tools (pinned in internal/tools/tools.go)..."
	$(GO) install github.com/golangci/golangci-lint/v2/cmd/golangci-lint
	$(GO) install golang.org/x/tools/cmd/goimports
	$(GO) install golang.org/x/vuln/cmd/govulncheck
	$(GO) install github.com/securego/gosec/v2/cmd/gosec
	$(GO) install github.com/google/go-licenses
	@echo "Done."

clean: ## Remove build artifacts.
	rm -rf bin dist
	rm -f $(COVERPROFILE) coverage.html

help: ## Show this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\nTargets:\n"} /^[a-zA-Z_-]+:.*?##/ { printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2 }' $(MAKEFILE_LIST)
