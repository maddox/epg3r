# All targets run inside Docker. Nothing is installed on the host.
UID := $(shell id -u)
GID := $(shell id -g)
export UID GID

COMPOSE := docker compose -f docker-compose.dev.yml
DEV     := $(COMPOSE) run --rm
IMAGE   ?= epg3r:dev
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

.PHONY: help cache test vet tidy fmt run sh build up logs stop reset down clean

help: ## List targets
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?## "}{printf "  %-10s %s\n", $$1, $$2}'

cache:
	@mkdir -p .cache/go

test: cache ## Run the test suite with the race detector
	$(DEV) -e CGO_ENABLED=1 dev go test -race ./...

vet: cache ## go vet
	$(DEV) dev go vet ./...

fmt: cache ## gofmt -l (fails if anything is unformatted)
	$(DEV) dev sh -c 'test -z "$$(gofmt -l cmd internal)" || (gofmt -l cmd internal; exit 1)'

tidy: cache ## go mod tidy
	$(DEV) dev go mod tidy

run: cache ## Run the server from source on :8080
	$(DEV) --service-ports dev go run ./cmd/epg3r serve

sh: cache ## Shell in the dev container
	$(DEV) dev bash

build: ## Build the production image
	docker build --build-arg VERSION=$(VERSION) -t $(IMAGE) .

up: ## Build and boot the app with the values in .env (copy .env.example first)
	@test -f .env || (echo "no .env file; run: cp .env.example .env  and edit it" && exit 1)
	docker compose up -d --build
	@echo "epg3r is up: http://localhost:$$(grep -E '^EPG3R_PORT=' .env | cut -d= -f2 | grep . || echo 8080)/healthz"

logs: ## Follow the app's logs
	docker compose logs -f epg3r

stop: ## Stop the app (keeps its data volume)
	docker compose down

reset: ## Stop the app and delete its data volume
	docker compose down -v

down: ## Stop the dev compose project
	$(COMPOSE) down --remove-orphans

clean: down ## Remove caches and local data
	rm -rf .cache data
