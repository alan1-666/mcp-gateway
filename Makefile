ifneq (,$(wildcard .env))
include .env
export
endif

.PHONY: bootstrap build migrate dev test check compose
bootstrap:
	node scripts/bootstrap.mjs

build:
	go build -o bin/ ./cmd/...

migrate:
	go run ./cmd/migrate

dev: build
	node --env-file-if-exists=.env scripts/dev.mjs

check:
	go vet ./...
	npm run check
	npm run build

test:
	go test -race ./...
	npm test

compose: bootstrap
	docker compose --env-file .local/compose.env -f deploy/compose/compose.yaml up --build
