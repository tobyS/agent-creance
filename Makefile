# agent-creance local task runner.
#
# Coming from PHP/TS: think of this as composer scripts / npm scripts, but the
# recipe lines MUST be indented with real tabs (Make is strict about that).
# `make` is preinstalled on macOS, so contributors need nothing extra except the
# Go toolchain. Run `make help` for the list.

# --- Build metadata stamped into the binary via -ldflags ----------------------
# `?=` means "only set if not already set", so CI/release can override.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

PKG     := github.com/tobyS/agent-creance/internal/buildinfo
LDFLAGS := -s -w \
	-X $(PKG).Version=$(VERSION) \
	-X $(PKG).Commit=$(COMMIT) \
	-X $(PKG).Date=$(DATE)

BIN_DIR := bin
BIN     := $(BIN_DIR)/agent-creance

# Pin tool versions used by `make tools` so every contributor gets the same one.
GOLANGCI_VERSION ?= v1.62.2

# Resolve golangci-lint whether or not GOBIN/GOPATH/bin is on PATH, so
# `make lint` works right after `make tools` without editing your shell profile.
GOLANGCI ?= $(shell command -v golangci-lint 2>/dev/null || echo $(shell go env GOPATH)/bin/golangci-lint)

.DEFAULT_GOAL := help

## help: list available targets
.PHONY: help
help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed -e 's/## //'

## build: compile the binary into bin/ with version metadata
.PHONY: build
build:
	@mkdir -p $(BIN_DIR)
	go build -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/agent-creance

## run: build and run (pass args via ARGS="doctor")
.PHONY: run
run:
	go run -ldflags "$(LDFLAGS)" ./cmd/agent-creance $(ARGS)

## test: fast, hermetic unit + script tests with the race detector on
.PHONY: test
test:
	go test -race ./...

## test-integration: slow tests that touch the real tools (build tag: integration)
.PHONY: test-integration
test-integration:
	go test -race -tags=integration ./...
	$(MAKE) test-enforcer-integration

# Python enforcer addon (the one piece of Python in the stack). It runs in its own
# repo-local venv with a pinned mitmproxy, and is kept OUT of the fast `make test`
# and the pre-commit hook so Go contributors without Python aren't blocked.
ENFORCER_DIR := internal/proxy/enforcer
ENFORCER_VENV := .venv-enforcer
ENFORCER_PYTEST := $(ENFORCER_VENV)/bin/pytest

.PHONY: enforcer-venv
enforcer-venv:
	@test -x $(ENFORCER_PYTEST) || python3 -m venv $(ENFORCER_VENV)
	@$(ENFORCER_VENV)/bin/pip install -q -r $(ENFORCER_DIR)/requirements.txt

## test-enforcer: Python mitmproxy addon tests (repo-local venv; pinned mitmproxy)
.PHONY: test-enforcer
test-enforcer: enforcer-venv
	$(ENFORCER_PYTEST) -q $(ENFORCER_DIR) -m "not integration"

## test-enforcer-integration: live mitmproxy + curl probes for the addon (gated S1)
.PHONY: test-enforcer-integration
test-enforcer-integration: enforcer-venv
	$(ENFORCER_PYTEST) -q $(ENFORCER_DIR) -m integration

## cover: run tests and open an HTML coverage report
.PHONY: cover
cover:
	go test -race -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out

## golden: regenerate golden test files (review the diff afterwards!)
# Only packages with a golden test define the -update flag; passing it to a test
# binary that doesn't define it is a hard error ("flag provided but not defined").
# So we discover the golden-bearing packages dynamically (by the shared flag
# description string) and run -update against just those, instead of `./...`.
.PHONY: golden
golden:
	@pkgs=$$(grep -rl 'regenerate golden files' --include='*_test.go' . | xargs -n1 dirname | sort -u); \
	if [ -z "$$pkgs" ]; then echo "no golden tests found"; exit 0; fi; \
	echo "regenerating goldens in:" $$pkgs; \
	go test $$pkgs -update

## fmt: format all Go code
.PHONY: fmt
fmt:
	gofmt -s -w .

## lint: run go vet and golangci-lint
.PHONY: lint
lint:
	go vet ./...
	$(GOLANGCI) run

## tidy: sync go.mod/go.sum
.PHONY: tidy
tidy:
	go mod tidy

## tools: install pinned dev tools (golangci-lint) into GOBIN
.PHONY: tools
tools:
	go install github.com/golangci/golangci-lint/cmd/golangci-lint@$(GOLANGCI_VERSION)

## hooks: install the git pre-commit hook (points core.hooksPath at .githooks)
.PHONY: hooks
hooks:
	git config core.hooksPath .githooks
	@echo "pre-commit hook enabled (.githooks/pre-commit)"

## clean: remove build and coverage artifacts
.PHONY: clean
clean:
	rm -rf $(BIN_DIR) coverage.out
