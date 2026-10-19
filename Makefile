# Mustard financial-planning batch jobs.

.PHONY: help build test lint fmt

help: ## Show targets
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-18s %s\n", $$1, $$2}'

build: ## Build the backtest and prices binaries into bin/
	go build -o bin/backtest .
	go build -o bin/prices ./prices

test: ## Unit tests (no network needed)
	go test ./...

lint: ## go vet
	go vet ./...

fmt: ## gofmt
	gofmt -w .
