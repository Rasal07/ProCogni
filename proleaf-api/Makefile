GO ?= go

.PHONY: build test vet fmt run mock migrate stats lint check db-up db-down

build:
	$(GO) build ./...

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

fmt:
	$(GO) fmt ./...

# What CI runs.
check: fmt vet test

run:
	$(GO) run ./cmd/api

# The API over in-memory fake data, for frontend work. No database needed.
mock:
	$(GO) run ./cmd/mock

migrate:
	$(GO) run ./cmd/api -migrate-only

# Recompute class aggregates for the current month.
stats:
	$(GO) run ./cmd/statsjob

db-up:
	docker compose up -d db

db-down:
	docker compose down
