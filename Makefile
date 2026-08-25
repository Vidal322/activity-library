# ---- config ----
BACKEND     := backend
BIN_DIR     := $(BACKEND)/bin
BINARY      := $(BIN_DIR)/api
PKG         := ./cmd/api

IMAGE       := activity-library-api
TAG         ?= dev
PORT        ?= 8080

VERSION     := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS     := -s -w -X main.version=$(VERSION)

GO          ?= go
# Compose reads .env from its own directory by default; the project keeps a
# single env file under backend/, so point it there explicitly.
COMPOSE     ?= docker compose --env-file $(BACKEND)/.env

.DEFAULT_GOAL := help

# ---- dev ----
.PHONY: run
run: ## Run the API locally
	$(GO) run -C $(BACKEND) $(PKG)

.PHONY: build
build: ## Build the API binary into backend/bin
	$(GO) build -C $(BACKEND) -trimpath -ldflags='$(LDFLAGS)' -o bin/api $(PKG)

.PHONY: clean
clean: ## Remove build output
	rm -rf $(BIN_DIR)

# ---- quality ----
.PHONY: test
test: ## Run tests
	$(GO) test -C $(BACKEND) -race ./...

.PHONY: cover
cover: ## Run tests and open an HTML coverage report
	$(GO) test -C $(BACKEND) -race -coverprofile=coverage.out ./...
	$(GO) tool -C $(BACKEND) cover -html=coverage.out

.PHONY: fmt
fmt: ## Format and simplify sources
	$(GO) fmt -C $(BACKEND) ./...

.PHONY: vet
vet: ## Run go vet
	$(GO) vet -C $(BACKEND) ./...

.PHONY: lint
lint: ## Run golangci-lint (if installed)
	@command -v golangci-lint >/dev/null || { echo "golangci-lint not installed"; exit 1; }
	golangci-lint run $(BACKEND)/...

.PHONY: tidy
tidy: ## Tidy and verify modules
	$(GO) mod tidy -C $(BACKEND)
	$(GO) mod verify -C $(BACKEND)

.PHONY: check
check: fmt vet test ## Everything CI should run

# ---- database ----
.PHONY: db-up
db-up: pgadmin-servers ## Start Postgres and pgAdmin, waiting until Postgres accepts connections
	$(COMPOSE) up -d --wait

.PHONY: db-down
db-down: ## Stop the database services (data is preserved)
	$(COMPOSE) down

.PHONY: db-reset
db-reset: ## Stop the database services and DELETE all data
	$(COMPOSE) down --volumes

.PHONY: pgadmin-servers
pgadmin-servers: ## Render docker/pgadmin/servers.json from DB_DSN
	@python3 scripts/pgadmin-servers.py

.PHONY: pgadmin-reset
pgadmin-reset: ## Wipe pgAdmin's state so servers.json is re-imported
	$(COMPOSE) rm -sf pgadmin
	docker volume rm -f activity_library_pgadmin-data
	$(COMPOSE) up -d --wait pgadmin

.PHONY: db-logs
db-logs: ## Follow the database service logs
	$(COMPOSE) logs -f

# ---- docker ----
.PHONY: docker-build
docker-build: ## Build the API image
	docker build -t $(IMAGE):$(TAG) $(BACKEND)

.PHONY: docker-run
docker-run: docker-build ## Run the API image (PORT=8080 to override)
	docker run --rm -p $(PORT):8080 --name $(IMAGE) $(IMAGE):$(TAG)

# ---- misc ----
.PHONY: health
health: ## Hit the health endpoint (liveness; does not touch the database)
	curl -fsS http://localhost:$(PORT)/health && echo

.PHONY: ready
ready: ## Hit the readiness endpoint (pings the database)
	@# --fail-with-body: exit non-zero on 503 while still printing the reason.
	curl -sS --fail-with-body http://localhost:$(PORT)/readyz && echo

.PHONY: help
help: ## Show this help
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) \
		| awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'
