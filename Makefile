.DEFAULT_GOAL := help

# Go lives outside PATH on some machines; point GO at it if needed:
#   make test GO=$$HOME/.local/go-sdk/bin/go
GO      ?= go
SQLC    ?= sqlc
BINARY  ?= bin/api
PKG     ?= ./...
VERSION ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo dev)

# Load .env for the local (non-Docker) targets so `make run` mirrors compose.
ifneq (,$(wildcard .env))
include .env
export
endif

.PHONY: help
help: ## Show the available targets
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-16s\033[0m %s\n", $$1, $$2}'

.PHONY: run
run: ## Run the API locally against the compose Postgres/Redis
	$(GO) run -ldflags="-X main.version=$(VERSION)" ./cmd/api

.PHONY: build
build: ## Compile the API binary into bin/
	@mkdir -p $(dir $(BINARY))
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags="-s -w -X main.version=$(VERSION)" -o $(BINARY) ./cmd/api

.PHONY: test
test: ## Run the full test suite
	$(GO) test $(PKG)

.PHONY: test-race
test-race: ## Run the test suite with the race detector
	$(GO) test -race $(PKG)

.PHONY: cover
cover: ## Run tests and write a coverage profile to coverage.out
	$(GO) test -coverprofile=coverage.out -covermode=atomic $(PKG)
	$(GO) tool cover -func=coverage.out | tail -1

.PHONY: vet
vet: ## Run go vet
	$(GO) vet $(PKG)

.PHONY: fmt
fmt: ## Format the source tree
	$(GO) fmt $(PKG)

.PHONY: tidy
tidy: ## Tidy go.mod / go.sum
	$(GO) mod tidy

.PHONY: check
check: fmt vet test ## Format, vet and test

.PHONY: sqlc
sqlc: ## Regenerate the sqlc query code from db/queries
	$(SQLC) generate

.PHONY: sqlc-install
sqlc-install: ## Install the sqlc generator into $$GOPATH/bin
	$(GO) install github.com/sqlc-dev/sqlc/cmd/sqlc@latest

.PHONY: env
env: ## Create .env from .env.example if it does not exist yet
	@test -f .env || (cp .env.example .env && echo "created .env from .env.example - review JWT_SECRET before deploying")

.PHONY: docker-up
docker-up: env ## Start the full stack (api + postgres + redis) in the background
	docker compose up -d --build

.PHONY: docker-deps
docker-deps: env ## Start only Postgres and Redis, for running the API on the host
	docker compose up -d postgres redis

.PHONY: docker-down
docker-down: ## Stop the stack, keeping the database volume
	docker compose down

.PHONY: docker-clean
docker-clean: ## Stop the stack and delete the database volume
	docker compose down -v

.PHONY: docker-logs
docker-logs: ## Tail the API logs
	docker compose logs -f api
