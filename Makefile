BINARY  := docker-gateway
IMAGE   := docker-gateway
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS := -ldflags="-s -w -X main.version=$(VERSION)"
DC_PROD := docker compose -f docker-compose.yml
DC_TEST := docker compose -f docker-compose.yml -f docker-compose.test.yml

.PHONY: build test lint clean docker-build \
        up up-build down logs \
        test-up test-up-build test-down test-logs \
        help

build: ## Compile the binary
	CGO_ENABLED=0 go build -mod=vendor $(LDFLAGS) -o $(BINARY) .

test: ## Run all unit tests with race detector
	go test -mod=vendor -race -count=1 -coverprofile=coverage.out ./...
	@go tool cover -func=coverage.out | tail -1

lint: ## Run golangci-lint
	golangci-lint run --timeout=5m ./...

clean: ## Remove build artifacts
	rm -f $(BINARY) coverage.out

docker-build: ## Build Docker image locally
	docker build --build-arg VERSION=$(VERSION) -t $(IMAGE):$(VERSION) .

## — Production stack ————————————————————————————————

up: ## Start the production stack in background
	$(DC_PROD) up -d

up-build: ## Build and start the production stack in background
	$(DC_PROD) up --build -d

down: ## Stop and remove the production stack
	$(DC_PROD) down

logs: ## Follow gateway logs (production)
	$(DC_PROD) logs -f gateway

## — Test stack ——————————————————————————————————————

test-up: ## Start the test stack in background
	$(DC_TEST) up -d

test-up-build: ## Build and start the test stack in background
	$(DC_TEST) up --build -d

test-down: ## Stop and remove the test stack
	$(DC_TEST) down

test-logs: ## Follow gateway logs (test stack)
	$(DC_TEST) logs -f gateway

## ————————————————————————————————————————————————————

help: ## Show available targets
	@grep -E '^[a-zA-Z_-]+:.*## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*## "}; {printf "  %-18s %s\n", $$1, $$2}'
