.PHONY: build test test-store lint check dev console-dev

build:
	go build -o bin/auth-gate ./cmd/auth-gate

test:
	go test ./...

test-store:
	docker compose up -d dynamodb
	DYNAMO_ENDPOINT=http://localhost:8000 go test ./...

lint:
	golangci-lint run

check:
	go vet ./...
	@# The Console reaches AuthGate over HTTP only. go list runs first on its own, so its failure fails the recipe.
	@deps=$$(go list -deps ./cmd/console) || exit 1; \
	if printf '%s\n' "$$deps" | grep -q internal/auth/service; then echo "cmd/console depends on internal/auth/service" >&2; exit 1; fi
	golangci-lint run
	go test ./...

dev:
	docker compose up -d && ROLE=dev TABLE_NAME=authgate-dev DYNAMO_ENDPOINT=http://localhost:8000 SIGNER=local ADMIN_KEY=dev-admin PORT=8080 BASE_URL=http://localhost:8080 go run ./cmd/auth-gate

console-dev:
	docker compose up -d dynamodb
	@test -f .env.console || { echo ".env.console is missing (ADMIN_KEY, CLIENT_KEY, AUTHGATE_CLIENT_ID)" >&2; exit 1; }
	set -a && . ./.env.console && set +a && CONSOLE_DEV=1 TABLE_NAME=console-dev DYNAMO_ENDPOINT=http://localhost:8000 AUTHGATE_BASE_URL=http://localhost:8080 go run ./cmd/console
