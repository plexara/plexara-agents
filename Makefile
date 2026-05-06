# Use bash for recipes — fuzz-quick uses process substitution and `pipefail`
# semantics that POSIX /bin/sh doesn't support.
SHELL       := /bin/bash
.SHELLFLAGS := -eu -o pipefail -c

.PHONY: all verify build test cover lint lint-config fmt fmt-check vet \
        sec vuln gosec semgrep semgrep-check tidy tidy-check mod-verify \
        build-matrix licenses fuzz-quick clean help

GO            ?= go
GOFLAGS       ?=
PKGS          ?= ./...
COVERPROFILE  ?= coverage.out
SEMGREP_IMAGE ?= semgrep/semgrep@sha256:326e5f41cc972bb423b764a14febbb62bbad29ee1c01820805d077dd868fea48
FUZZTIME      ?= 5s

# Build matrix mirrors ci.yml — keep in sync.
BUILD_MATRIX = \
  linux/amd64 \
  linux/arm64 \
  darwin/amd64 \
  darwin/arm64 \
  windows/amd64

# Dev tools are pinned via the `tool` directive in go.mod (Go 1.24+) and
# invoked through `go tool <name>`. Run `go mod tidy` after pulling to
# materialize them locally; no separate install step is required.

# `make verify` is the canonical "ready to push" gate. It runs every
# check CI runs that can run locally. If this passes, the odds of CI
# failing are very low; if it fails, do not push.
verify: fmt-check tidy-check mod-verify vet lint-config lint test build-matrix sec semgrep-check fuzz-quick
	@echo ""
	@echo "  ===================================="
	@echo "   make verify: PASS"
	@echo "   $$(date -u +'%Y-%m-%dT%H:%M:%SZ') on $$(uname -sm)"
	@echo "  ===================================="

all: build lint test ## Run build, lint, and test.

build: ## Build all packages for the host platform.
	@out=$$($(GO) build $(GOFLAGS) $(PKGS) 2>&1); \
		ec=$$?; \
		if [ -n "$$out" ]; then echo "$$out"; fi; \
		if echo "$$out" | grep -q "matched no packages"; then \
			echo "(no Go packages yet — see issues #4 onward)"; \
		fi; \
		exit $$ec

build-matrix: ## Cross-compile across the same matrix CI builds.
	@for target in $(BUILD_MATRIX); do \
		os=$${target%%/*}; arch=$${target##*/}; \
		printf '  build %-15s ... ' "$$os/$$arch"; \
		out=$$(CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch $(GO) build -trimpath $(PKGS) 2>&1); \
		ec=$$?; \
		if [ $$ec -ne 0 ]; then echo "FAIL"; echo "$$out"; exit $$ec; fi; \
		echo "ok"; \
	done

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

lint-config: ## Verify .golangci.yml against the v2 schema.
	$(GO) tool golangci-lint config verify

fmt: ## Format Go code.
	$(GO) tool goimports -w -local github.com/plexara/plexara-agents .

fmt-check: ## Fail if any Go file is unformatted.
	@unformatted=$$(gofmt -l .); \
	if [ -n "$$unformatted" ]; then \
		echo "Unformatted files:"; echo "$$unformatted"; \
		echo "Run 'make fmt' and commit."; \
		exit 1; \
	fi

vet: ## Run go vet.
	$(GO) vet $(PKGS)

sec: gosec vuln ## Run security scanners (gosec + govulncheck).

gosec: ## Run gosec.
	$(GO) tool gosec -quiet -no-fail $(PKGS)

vuln: ## Run govulncheck (skips silently if no Go source yet).
	@if find . -name '*.go' \
	         -not -path './.*' \
	         -not -path './vendor/*' \
	         -not -path '*/testdata/*' \
	         -print -quit | grep -q .; then \
		$(GO) tool govulncheck $(PKGS); \
	else \
		echo "(no Go source yet — skipping govulncheck)"; \
	fi

semgrep: ## Run Semgrep in the same Docker image CI uses.
	@if ! command -v docker >/dev/null 2>&1; then \
		echo "docker not installed — cannot run semgrep locally"; \
		exit 1; \
	fi
	@if ! docker info >/dev/null 2>&1; then \
		echo "docker daemon not running — cannot run semgrep locally"; \
		exit 1; \
	fi
	docker run --rm -v "$$PWD:/src" -w /src $(SEMGREP_IMAGE) \
		semgrep \
			--config=p/security-audit \
			--config=p/secrets \
			--config=p/golang \
			--config=p/owasp-top-ten \
			--error \
			.

semgrep-check: ## Run semgrep if Docker is available; otherwise warn and continue.
	@if command -v docker >/dev/null 2>&1 && docker info >/dev/null 2>&1; then \
		$(MAKE) --no-print-directory semgrep; \
	else \
		echo ""; \
		echo "  WARNING: semgrep skipped (Docker not available)."; \
		echo "  CI will run it; you cannot prove a clean run without Docker."; \
		echo ""; \
	fi

mod-verify: ## go mod verify.
	$(GO) mod verify

tidy: ## Tidy go.mod and verify modules.
	$(GO) mod tidy
	$(GO) mod verify

tidy-check: ## Fail if go.mod / go.sum drift from `go mod tidy`.
	@diff=$$($(GO) mod tidy -diff); \
	if [ -n "$$diff" ]; then \
		echo "go.mod / go.sum drift detected; run 'make tidy' and commit."; \
		echo "$$diff"; \
		exit 1; \
	fi

fuzz-quick: ## Run each Fuzz* target for FUZZTIME (default 5s).
	@found=0; \
	while IFS= read -r pkg; do \
		[ -z "$$pkg" ] && continue; \
		while IFS= read -r fuzz; do \
			[ -z "$$fuzz" ] && continue; \
			found=1; \
			printf '  fuzz %s/%s for %s ... ' "$$pkg" "$$fuzz" "$(FUZZTIME)"; \
			out=$$($(GO) test "$$pkg" -run='^$$' -fuzz="^$$fuzz\$$" -fuzztime=$(FUZZTIME) 2>&1); \
			ec=$$?; \
			if [ $$ec -ne 0 ]; then echo "FAIL"; echo "$$out"; exit $$ec; fi; \
			echo "ok"; \
		done < <($(GO) test -list 'Fuzz.*' "$$pkg" 2>/dev/null | awk '/^Fuzz/'); \
	done < <($(GO) list ./... 2>/dev/null); \
	if [ $$found -eq 0 ]; then \
		echo "(no Fuzz* targets discovered — skipping)"; \
	fi

licenses: ## Report on transitive dependency licenses.
	$(GO) tool go-licenses report $(PKGS)

clean: ## Remove build artifacts.
	rm -rf bin dist
	rm -f $(COVERPROFILE) coverage.html

help: ## Show this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\nTargets:\n"} /^[a-zA-Z_-]+:.*?##/ { printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2 }' $(MAKEFILE_LIST)
