.PHONY: build test test-integration lint run up down cover

TEST_DB ?= postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable

build:
	go build -o bin/ ./cmd/...

test:
	go test -race ./...

test-integration:
	LEDGER_TEST_DATABASE_URL=$(TEST_DB) go test -race -count=1 ./...

cover:
	LEDGER_TEST_DATABASE_URL=$(TEST_DB) go test -race -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out | tail -1

lint:
	golangci-lint run

run:
	LEDGER_STORE=memory go run ./cmd/ledgerd

up:
	docker compose up --build -d

down:
	docker compose down -v
