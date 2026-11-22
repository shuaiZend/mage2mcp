# mage2mcp developer commands. Run `make help` for the list.
GO ?= go
VERSION ?= dev
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: help build test test-race lint fmt vet cover docker docs-check check-test-inventory

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-12s %s\n", $$1, $$2}'

build: ## Build the server, worker, and admin binaries
	$(GO) build -trimpath -ldflags "$(LDFLAGS)" -o bin/mage2mcp ./cmd/server
	$(GO) build -trimpath -ldflags "$(LDFLAGS)" -o bin/mage2mcp-worker ./cmd/worker
	$(GO) build -trimpath -ldflags "$(LDFLAGS)" -o bin/mage2mcp-admin ./cmd/admin

test: ## Run unit tests
	$(GO) test ./...

test-race: ## Run tests with the race detector
	$(GO) test -race ./...

cover: ## Write coverage.out and print the total
	@# Only packages that have tests: `go test -cover` on a package without any needs
	@# the covdata tool to emit an empty profile, and a toolchain that omits it fails
	@# the whole target for three packages that have no coverage to contribute.
	$(GO) test -covermode=atomic -coverprofile=coverage.out $$($(GO) list -f '{{if or .TestGoFiles .XTestGoFiles}}{{.ImportPath}}{{end}}' ./...)
	$(GO) tool cover -func=coverage.out | tail -n 1

lint: ## Run golangci-lint
	$(GO) run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0 run

fmt: ## Format Go sources
	$(GO) fmt ./...

vet: ## Run go vet
	$(GO) vet ./...

docker: ## Build the container image
	docker build --build-arg VERSION=$(VERSION) -t mage2mcp:$(VERSION) .

docs-check: ## Lint documentation and design contracts
	npm ci
	npm run lint:md
	npm run lint:links
	npm run test:contracts

check-test-inventory: ## Fail when a test function disappeared since origin/main
	@# A deleted test cannot fail, so the suite stays green without it. This gate
	@# compares test names between origin/main and HEAD and fails on a removal;
	@# a deliberate deletion is recorded with scripts/check-test-inventory.sh
	@# --allow-removed <TestName>.
	bash scripts/check-test-inventory.sh

.PHONY: e2e
e2e: ## Run the end-to-end Magento smoke test (needs Docker and Go)
	./test/e2e/run.sh
