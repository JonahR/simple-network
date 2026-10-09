.PHONY: up down logs test run-pos

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
