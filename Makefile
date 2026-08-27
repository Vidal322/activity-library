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

# The database the test harness connects to. The harness refuses any name not
# ending in _test, because it truncates every table it finds.
DB_TEST_NAME ?= activity_library_test

MIGRATIONS  := internal/store/migrations
# The goose CLI compiles in one driver per database it supports. Exclude every
# dialect but postgres: nothing else is needed, and each one drags in its own
# driver dependency.
GOOSE_TAGS  := no_clickhouse no_libsql no_mssql no_mysql no_sqlite3 no_vertica no_ydb
# Pinned to whatever version go.mod already uses for the goose library, so the
# CLI that writes migrations cannot drift from the code that applies them.
GOOSE_VERSION = $(shell $(GO) list -C $(BACKEND) -m -f '{{.Version}}' github.com/pressly/goose/v3)
# Compose reads .env from its own directory by default; the project keeps a
# single env file under backend/, so point it there explicitly.
COMPOSE     ?= docker compose --env-file $(BACKEND)/.env
# The network docker-run joins so the API container can resolve the "postgres"
# service by name. Compose names it <project>_default, but the project name
# depends on COMPOSE_PROJECT_NAME and on this directory's name, so it is read
# back from the running container rather than reconstructed. ?= defines a
# recursively expanded variable, so this only shells out if docker-run is the
# goal, by which point its db-up prerequisite has started the container.
DB_NETWORK  ?= $(shell docker inspect $$($(COMPOSE) ps -q postgres 2>/dev/null) \
                   --format '{{range $$k, $$v := .NetworkSettings.Networks}}{{$$k}}{{"\n"}}{{end}}' \
                   2>/dev/null | head -1)

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
# -p 1 runs one package at a time. The database-backed tests truncate every
# table at setup, so two packages running at once would wipe each other's rows
# mid-test. The alternative, a database per package, buys back the parallelism
# at the cost of provisioning and naming one database per package; revisit it if
# the suite ever gets slow enough to care.
.PHONY: test
test: ## Run tests (set DB_TEST_DSN to include the database-backed ones)
	$(GO) test -C $(BACKEND) -race -p 1 ./...

.PHONY: cover
cover: ## Run tests and open an HTML coverage report
	$(GO) test -C $(BACKEND) -race -p 1 -coverprofile=coverage.out ./...
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

.PHONY: seed
seed: db-up ## Insert the development dataset (idempotent; local databases only)
	@# Runs migrations itself, so this works against a freshly reset database
	@# without starting the API first.
	$(GO) run -C $(BACKEND) ./cmd/seed

.PHONY: db-test-create
db-test-create: db-up ## Create the test database and print the DB_TEST_DSN to export
	@# The API's own migrations run against it from TestMain, so an empty
	@# database is all this has to produce.
	@# DB_TEST_DSN is printed rather than written to backend/.env on purpose:
	@# godotenv loads that file at startup, and a truncating harness should
	@# never be one stray import away from the development database.
	@set -a && . ./$(BACKEND)/.env && set +a && \
		{ $(COMPOSE) exec -T postgres createdb -U "$$POSTGRES_USER" $(DB_TEST_NAME) 2>/dev/null \
			|| echo "database $(DB_TEST_NAME) already exists"; } && \
		echo "" && \
		echo "Export this before running the database-backed tests:" && \
		echo "  export DB_TEST_DSN=\"$${DB_DSN%/*}/$(DB_TEST_NAME)?sslmode=disable\""

.PHONY: db-test-drop
db-test-drop: ## Delete the test database
	@set -a && . ./$(BACKEND)/.env && set +a && \
		$(COMPOSE) exec -T postgres dropdb --if-exists -U "$$POSTGRES_USER" $(DB_TEST_NAME)

.PHONY: db-logs
db-logs: ## Follow the database service logs
	$(COMPOSE) logs -f

# ---- migrations ----
# The API applies migrations itself at startup; these targets are for authoring
# them and for inspecting or unwinding schema state by hand.
.PHONY: goose
goose: ## Build the goose CLI into backend/bin (postgres only)
	@# Installed by version rather than built from ./backend: the pkg@version
	@# form resolves in its own module context, so the CLI's driver
	@# dependencies stay out of the API's go.mod and go.sum.
	GOBIN=$(abspath $(BIN_DIR)) $(GO) install -tags '$(GOOSE_TAGS)' \
		github.com/pressly/goose/v3/cmd/goose@$(GOOSE_VERSION)

.PHONY: migration
migration: goose ## Scaffold a migration (name=add_activities)
	@test -n "$(name)" || { echo "usage: make migration name=add_activities"; exit 1; }
	cd $(BACKEND) && ./bin/goose -s -dir $(MIGRATIONS) create $(name) sql

.PHONY: migrate-status
migrate-status: goose ## Show which migrations have been applied
	cd $(BACKEND) && set -a && . ./.env && set +a && ./bin/goose -dir $(MIGRATIONS) postgres "$$DB_DSN" status

.PHONY: migrate-down
migrate-down: goose ## Roll back the most recent migration
	cd $(BACKEND) && set -a && . ./.env && set +a && ./bin/goose -dir $(MIGRATIONS) postgres "$$DB_DSN" down

# ---- docker ----
.PHONY: docker-build
docker-build: ## Build the API image
	docker build -t $(IMAGE):$(TAG) $(BACKEND)

.PHONY: docker-run
docker-run: docker-build db-up ## Run the API image against the Compose database (PORT=8080 to override)
	@# .env is deliberately kept out of the image (see .dockerignore), so the
	@# config is passed in at run time. DB_DSN names localhost, which is correct
	@# from the host but points at the API container itself once inside one;
	@# rewriting the host keeps the credentials in .env as the single source.
	set -a && . ./$(BACKEND)/.env && set +a && \
	net='$(DB_NETWORK)' && \
	{ test -n "$$net" || { echo "make: could not read the Compose network from the postgres container; is it running? (make db-up)" >&2; exit 1; }; } && \
	docker run --rm -p $(PORT):8080 --name $(IMAGE) \
		--network "$$net" \
		--env-file $(BACKEND)/.env \
		-e ADDR=:8080 \
		-e DB_DSN="$$(printf '%s' "$$DB_DSN" | sed 's#@localhost:#@postgres:#')" \
		$(IMAGE):$(TAG)

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
