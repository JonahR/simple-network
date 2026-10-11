.PHONY: up down logs test run-pos serve serve-stop hooks reset-data

up: ## Build and start all services in Docker
	docker compose up --build -d

down: ## Stop all services
	docker compose down

logs: ## Follow service logs
	docker compose logs -f

test: ## Run unit tests
	go test ./...

run-pos: ## Run the POS locally without Docker
	go run ./cmd/pos

serve: ## Always run the newest commit of main that builds and passes tests (refresh to see it)
	scripts/serve-latest.sh

serve-stop: ## Stop make serve and its services
	@pid=$$(cat "$$(dirname "$$(git rev-parse --path-format=absolute --git-common-dir)")/.run/serve.pid" 2>/dev/null) && kill $$pid && echo "stopped serve-latest (pid $$pid)" || echo "serve-latest is not running"

reset-data: ## Delete saved state (stop make serve first, or the running services save it again)
	rm -rf "$$(dirname "$$(git rev-parse --path-format=absolute --git-common-dir)")/.run/data" data cmd/*/data
	@echo "saved state deleted; start the services again for a clean slate (Docker: docker compose down -v)"

hooks: ## Block commits that don't build or pass tests (applies to every worktree)
	git config core.hooksPath .githooks
	@echo "pre-commit hook enabled"
