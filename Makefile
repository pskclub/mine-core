# mine-core: two Go modules in one repository.
#
#   v2/   github.com/pskclub/mine-core/v2   active -- every unsuffixed target
#   ./    github.com/pskclub/mine-core      v1, maintenance only -- *-v1 targets
#
# `./...` at the root never descends into v2/: a nested module is invisible to
# its parent's package patterns, so v2 targets run from inside v2/.
#
# `make help` lists the targets. `make check` is what to run before a PR.

# Pinned here and in .github/workflows/ci.yml -- bump both together.
GOLANGCI_LINT_VERSION := v2.13.2
GOVULNCHECK_VERSION   := latest

LEVEL       ?= patch
GOTESTFLAGS ?=

PYTHON  := $(shell command -v python3 2>/dev/null || command -v python 2>/dev/null)
COMPOSE := docker compose -f v2/compose.test.yaml

# How the integration tests find what `make services-up` starts. They read the
# same APP_* variables a real service does; see v2/compose.test.yaml.
INTEGRATION_ENV := \
	APP_CACHE_HOST=127.0.0.1:6379 \
	APP_DB_MONGO_HOST=127.0.0.1 \
	APP_DB_MONGO_NAME=coretest \
	APP_DB_MONGO_REPLICA_NAME=rs0 \
	APP_S3_ENDPOINT=127.0.0.1:9000 \
	APP_S3_ACCESS_KEY=minioadmin \
	APP_S3_SECRET_KEY=minioadmin \
	APP_S3_BUCKET=coretest \
	APP_S3_REGION=us-east-1 \
	APP_S3_FORCE_PATH_STYLE=true \
	APP_DB_DRIVER=postgres \
	APP_DB_HOST=127.0.0.1 \
	APP_DB_PORT=5432 \
	APP_DB_USER=coretest \
	APP_DB_PASSWORD=coretest \
	APP_DB_NAME=coretest \
	APP_DB_SSLMODE=disable \
	TEST_DATABASE_URL='postgres://coretest:coretest@127.0.0.1:5432/coretest?sslmode=disable'

.DEFAULT_GOAL := help
.PHONY: help check test test-race test-integration test-v1 services-up services-down \
        lint fmt tidy tidy-check vuln links tag tag-v1

help: ## Show this help
	@awk 'BEGIN { FS = ":.*## " } /^[a-zA-Z0-9_-]+:.*## / { printf "  %-18s %s\n", $$1, $$2 }' $(MAKEFILE_LIST)

check: ## Everything the required CI checks run, for both modules
	cd v2 && go build ./... && go vet ./...
	$(MAKE) test-race lint tidy-check links test-v1

# ---- v2 ---------------------------------------------------------------------

test: ## v2 unit tests
	cd v2 && go test $(GOTESTFLAGS) ./...

test-race: ## v2 unit tests with the race detector, as CI runs them
	cd v2 && CGO_ENABLED=1 go test -race $(GOTESTFLAGS) ./...

test-integration: ## v2 unit + integration tests (start `make services-up` first)
	cd v2 && $(INTEGRATION_ENV) CGO_ENABLED=1 go test -race -count=1 -tags=integration $(GOTESTFLAGS) ./...

services-up: ## Start redis, mongo (replica set), minio and postgres; waits until usable
	$(COMPOSE) up -d --wait

services-down: ## Stop the integration services and delete their data
	$(COMPOSE) down -v

lint: ## golangci-lint on v2, at the version CI pins
	cd v2 && go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION) run ./...

fmt: ## gofmt v2
	cd v2 && gofmt -w .

tidy: ## go mod tidy on v2
	cd v2 && go mod tidy

tidy-check: ## Fail if v2's go.mod / go.sum are not tidy
	cd v2 && go mod tidy -diff

# ---- v1 (maintenance) -------------------------------------------------------

test-v1: ## v1 vet + unit tests with the race detector
	go vet ./...
	CGO_ENABLED=1 go test -race $(GOTESTFLAGS) ./...

# ---- repository -------------------------------------------------------------

vuln: ## govulncheck both modules
	cd v2 && go run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...
	go run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...

links: ## Check relative links in every markdown file
	$(PYTHON) scripts/check-links.py

tag: ## Tag + push the next v2 release (LEVEL=patch|minor); prefer the Tag release workflow
	./tag v2 $(LEVEL)

tag-v1: ## Tag + push the next v1 release (LEVEL=patch|minor)
	./tag v1 $(LEVEL)
