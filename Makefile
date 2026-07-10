.DEFAULT_GOAL := help
.PHONY: help up down logs migrate seed test test-integration lint fmt sqlc

# sqlc is pinned to a container so codegen is reproducible without a host install.
SQLC_IMAGE := sqlc/sqlc:1.29.0
# The race detector needs cgo and a C toolchain, which a stock Windows host does
# not have. Running tests in the Go image makes `make test` behave identically
# on every machine, which is the point.
GO_IMAGE := golang:1.25
DOCKER_GO := docker run --rm -v "$(CURDIR)/backend:/src" -v linkr_gomod:/go/pkg/mod -w /src $(GO_IMAGE)

help: ## Show this help
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'

.env: ## Create .env from the committed example if it does not exist
	@test -f .env || (cp .env.example .env && echo "created .env from .env.example")

up: .env ## Build and start the full stack
	docker compose up --build

down: ## Stop the stack (add ARGS=-v to also drop volumes)
	docker compose down $(ARGS)

logs: ## Tail logs from all services
	docker compose logs -f

migrate: .env ## Run pending migrations against the compose Postgres
	docker compose run --rm backend /server -migrate

seed: .env ## Insert the demo user and sample links with backdated clicks
	docker compose run --rm backend /server -seed

test: ## Unit tests, race detector on, no external dependencies
	$(DOCKER_GO) go test -race ./...

test-host: ## Unit tests on the host toolchain (no race detector without cgo)
	cd backend && go test ./...

test-integration: .env ## Integration tests against real Postgres + Redis
	docker compose up -d postgres redis
	@echo "waiting for dependencies..."
	@until [ "$$(docker compose ps -q postgres | xargs docker inspect -f '{{.State.Health.Status}}')" = "healthy" ]; do sleep 1; done
	docker run --rm --network linkr_linkr_net \
		-v "$(CURDIR)/backend:/src" -v linkr_gomod:/go/pkg/mod -w /src \
		-e DATABASE_URL="postgres://$${POSTGRES_USER:-linkr}:$${POSTGRES_PASSWORD:-linkr_dev_password}@postgres:5432/$${POSTGRES_DB:-linkr}?sslmode=disable" \
		-e REDIS_URL="redis://redis:6379/1" \
		$(GO_IMAGE) go test -race -tags=integration -v ./tests/...

lint: ## go vet + gofmt check
	cd backend && go vet ./...
	@cd backend && test -z "$$(gofmt -l . | grep -v '^internal/db/')" || \
		(echo "gofmt needed:" && gofmt -l . | grep -v '^internal/db/' && exit 1)

fmt: ## Format Go sources
	cd backend && gofmt -w $$(find . -name '*.go' -not -path './internal/db/*')

sqlc: ## Regenerate internal/db from migrations/ + queries/
	docker run --rm -v "$(CURDIR)/backend:/src" -w /src $(SQLC_IMAGE) generate
