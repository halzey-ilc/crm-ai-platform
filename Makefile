SHELL := /bin/bash
COMPOSE := docker compose --env-file .env -f deployments/local/compose.yaml
TOOLKIT := training/toolkit

.PHONY: help env up up-llm down logs ps build test test-integration lint vet fmt-check docker-build \
        toolkit-install toolkit-test toolkit-experiment check token

help: ## List targets
	@grep -E '^[a-z-]+:.*##' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-18s %s\n", $$1, $$2}'

env: ## Create .env with generated secrets (never overwrites)
	./scripts/init_env.sh

up: ## Start postgres, migrate, gateway, orchestrator
	$(COMPOSE) up -d --build

up-llm: ## Start the stack with the local model (Qwen3.5-4B on llama.cpp) instead of the mock
	./scripts/get_model.sh
	$(COMPOSE) -f deployments/local/compose.llm.yaml up -d --build

down: ## Stop the stack (keeps the database volume)
	$(COMPOSE) -f deployments/local/compose.llm.yaml down

logs: ## Follow service logs
	$(COMPOSE) logs -f gateway orchestrator

ps: ## Show service status
	$(COMPOSE) ps

build: ## Build Go binaries into ./bin
	go build -o bin/ ./services/... ./tools/...

test: ## Go unit tests (no external services)
	go test -race ./...

test-integration: ## Go integration + tenant-isolation tests (needs `make up` for postgres)
	set -a; source .env; set +a; \
	AI_TEST_OWNER_URL="postgres://ai_owner:$${AI_DB_OWNER_PASSWORD}@127.0.0.1:55432/postgres?sslmode=disable" \
	go test -race -count=1 $(GOTESTFLAGS) -tags integration ./tests/integration/...

vet: ## go vet (including integration-tagged code)
	go vet ./... && go vet -tags integration ./tests/...

fmt-check: ## Fail if any Go file is not gofmt-formatted
	@test -z "$$(gofmt -l .)" || (gofmt -l .; exit 1)

lint: fmt-check vet ## Go static checks

docker-build: ## Build all runtime images
	for t in gateway orchestrator migrate; do \
	  docker build -f deployments/docker/Dockerfile --target $$t -t crm-ai-platform/$$t:local . || exit 1; done

token: ## Mint a local dev token: make token TENANT=demo
	@set -a; source .env; set +a; go run ./tools/devtoken -tenant $${TENANT:-demo}

toolkit-install: ## Install the Python training toolkit (uv)
	cd $(TOOLKIT) && uv sync --all-groups

toolkit-test: ## Lint and test the training toolkit
	cd $(TOOLKIT) && uv run ruff check . && uv run pytest -q

toolkit-experiment: ## Run the small synthetic lead-scoring experiment
	cd $(TOOLKIT) && uv run crmai-train run experiments/lead_scoring_synthetic.toml

check: lint test toolkit-test ## Everything that needs no running services
