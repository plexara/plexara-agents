.PHONY: all build test lint sec cover tidy fmt vet vuln licenses clean help

GO        ?= go
GOFLAGS   ?=
PKGS      ?= ./...
COVERPROFILE ?= coverage.out

# Dev tools are pinned via the `tool` directive in go.mod (Go 1.24+) and
# invoked through `go tool <name>`. Run `go mod tidy` after pulling to
# materialize them locally; no separate install step is required.

all: build lint test ## Run build, lint, and test.

build: ## Build all packages.
	@out=$$($(GO) build $(GOFLAGS) $(PKGS) 2>&1); \
		ec=$$?; \
		if [ -n "$$out" ]; then echo "$$out"; fi; \
		if echo "$$out" | grep -q "matched no packages"; then \
			echo "(no Go packages yet — see issues #4 onward)"; \
		fi; \
		exit $$ec

test: ## Run tests with race detector, shuffle, and coverage.
	$(GO) test -race -shuffle=on -count=1 \
		-covermode=atomic -coverprofile=$(COVERPROFILE) \
		$(PKGS)

cover: test ## Show coverage summary and write coverage.html.
	$(GO) tool cover -func=$(COVERPROFILE) | tail -n 1
	$(GO) tool cover -html=$(COVERPROFILE) -o coverage.html
	@echo "Wrote coverage.html"

lint: ## Run golangci-lint.
	$(GO) tool golangci-lint run $(PKGS)

fmt: ## Format Go code.
	$(GO) tool goimports -w -local github.com/plexara/plexara-agents .

vet: ## Run go vet.
	$(GO) vet $(PKGS)

sec: vuln ## Run security scanners (gosec + govulncheck).
	$(GO) tool gosec -quiet $(PKGS)

vuln: ## Run govulncheck.
	$(GO) tool govulncheck $(PKGS)

licenses: ## Report on transitive dependency licenses.
	$(GO) tool go-licenses report $(PKGS)

tidy: ## Tidy go.mod and verify modules.
	$(GO) mod tidy
	$(GO) mod verify

clean: ## Remove build artifacts.
	rm -rf bin dist
	rm -f $(COVERPROFILE) coverage.html

help: ## Show this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\nTargets:\n"} /^[a-zA-Z_-]+:.*?##/ { printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2 }' $(MAKEFILE_LIST)
