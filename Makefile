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

# ---- docker ----
.PHONY: docker-build
docker-build: ## Build the API image
	docker build -t $(IMAGE):$(TAG) $(BACKEND)

.PHONY: docker-run
docker-run: docker-build ## Run the API image (PORT=8080 to override)
	docker run --rm -p $(PORT):8080 --name $(IMAGE) $(IMAGE):$(TAG)

# ---- misc ----
.PHONY: health
health: ## Hit the health endpoint
	curl -fsS http://localhost:$(PORT)/health && echo

.PHONY: help
help: ## Show this help
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) \
		| awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'
