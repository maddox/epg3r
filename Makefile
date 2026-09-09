# All targets run inside Docker. Nothing is installed on the host.
UID := $(shell id -u)
GID := $(shell id -g)
export UID GID

COMPOSE := docker compose -f docker-compose.dev.yml
DEV     := $(COMPOSE) run --rm
IMAGE   ?= epg3r:dev
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

.PHONY: help cache need-env test vet tidy fmt run reset-dev sh build up logs stop reset down clean css css-check logo-ids font

help: ## List targets
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?## "}{printf "  %-10s %s\n", $$1, $$2}'

cache:
	@mkdir -p .cache/go

test: cache ## Run the test suite with the race detector
	$(DEV) -e CGO_ENABLED=1 dev go test -race ./...

vet: cache ## go vet
	$(DEV) dev go vet ./...

fmt: cache ## gofmt -l (fails if anything is unformatted)
	$(DEV) dev sh -c 'test -z "$$(gofmt -l cmd internal scripts)" || (gofmt -l cmd internal scripts; exit 1)'

tidy: cache ## go mod tidy
	$(DEV) dev go mod tidy

css: cache ## Build internal/web/static/app.css from the Tailwind source (commit the result)
	$(DEV) dev sh scripts/tailwind.sh

css-check: css ## Fail if the committed app.css is out of date
	@git diff --quiet -- internal/web/static/app.css || (echo "internal/web/static/app.css is stale; run make css and commit" && exit 1)

need-env:
	@test -f .env || (echo "no .env file; run: cp .env.example .env  and edit it" && exit 1)

run: cache ## Run from source with hot reload; seeds from .env when present (see ./dev)
	$(DEV) --service-ports dev go run github.com/air-verse/air@v1.61.7 -c .air.toml

reset-dev: ## Delete the dev database and caches under ./data
	rm -rf data

sh: cache ## Shell in the dev container
	$(DEV) dev bash

build: ## Build the production image
	docker build --build-arg VERSION=$(VERSION) -t $(IMAGE) .

up: need-env ## Build and boot the app with the values in .env (copy .env.example first)
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

font: cache ## Vendor the art typeface into internal/art/data (run by hand; commit the result)
	$(DEV) dev sh scripts/font.sh

logo-ids: cache ## Resolve team logo ids into the catalog (run by hand; commit the result)
	$(DEV) dev go run ./scripts/logoids
