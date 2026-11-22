.PHONY: build test test-store lint check dev console-dev console-build portal-dev portal-build

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
	@# The Portal reaches AuthGate over HTTP only and shares no code with the Console but internal/platform.
	@if [ -d cmd/portal ]; then \
		deps=$$(go list -deps ./cmd/portal) || exit 1; \
		for p in internal/auth/service internal/console; do \
			if printf '%s\n' "$$deps" | grep -q "auth-gate/$$p\$$\|auth-gate/$$p/"; then echo "cmd/portal depends on $$p" >&2; exit 1; fi; \
		done; \
	fi
	golangci-lint run
	go test ./...

dev:
	docker compose up -d && ROLE=dev TABLE_NAME=authgate-dev DYNAMO_ENDPOINT=http://localhost:8000 SIGNER=local ADMIN_KEY=dev-admin PORT=8080 BASE_URL=http://localhost:8080 go run ./cmd/auth-gate

console-dev:
	docker compose up -d dynamodb
	@test -f .env.console || { echo ".env.console is missing (ADMIN_KEY, CLIENT_KEY, AUTHGATE_CLIENT_ID)" >&2; exit 1; }
	set -a && . ./.env.console && set +a && CONSOLE_DEV=1 TABLE_NAME=console-dev DYNAMO_ENDPOINT=http://localhost:8000 AUTHGATE_BASE_URL=http://localhost:8080 go run ./cmd/console

# Same flags as scripts/setup-and-deploy/02-build.sh: linux/arm64, static, zipped for SAM.
console-build:
	mkdir -p bin/console
	rm -f bin/console/bootstrap bin/console/bootstrap.zip
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o bin/console/bootstrap ./cmd/console
	cd bin/console && zip -q bootstrap.zip bootstrap

portal-dev:
	docker compose up -d dynamodb
	@test -f .env.portal || { echo ".env.portal is missing (AUTHGATE_CLIENT_ID, CLIENT_KEY); a later task's register-client.sh local writes it, like .env.console" >&2; exit 1; }
	set -a && . ./.env.portal && set +a && PORTAL_DEV=1 TABLE_NAME=portal-dev DYNAMO_ENDPOINT=http://localhost:8000 AUTHGATE_BASE_URL=http://localhost:8080 PORT=8789 MAILBOX_DIR=.portal-mailbox go run ./cmd/portal

# Same flags as console-build.
portal-build:
	mkdir -p bin/portal
	rm -f bin/portal/bootstrap bin/portal/bootstrap.zip
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o bin/portal/bootstrap ./cmd/portal
	cd bin/portal && zip -q bootstrap.zip bootstrap
