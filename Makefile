SHELL   := bash
BIN_DIR := bin

# Every subdir of cmd/ is a buildable app. APP selects one (default: first).
APPS := $(notdir $(wildcard cmd/*))
APP  ?= $(firstword $(APPS))

.PHONY: help run test cover vet lint fmt check tidy build build-all clean oapicodegen

help: ## List targets
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?## "}{printf "  %-12s %s\n", $$1, $$2}'
	@echo "  apps: $(APPS)"

run: ## Run an app (APP=name, default $(APP))
	go run ./cmd/$(APP)

test: ## Run tests with race detector
	go test -race ./...

cover: ## Run tests and open coverage report
	go test -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out

vet: ## go vet
	go vet ./...

lint: ## golangci-lint (go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest)
	golangci-lint run

fmt: ## Format and fix imports (go install golang.org/x/tools/cmd/goimports@latest)
	goimports -w .

tidy: ## Sync go.mod/go.sum
	go mod tidy

check: fmt vet lint test ## Format, vet, lint, test

build: ## Build every app for the host OS
	@for app in $(APPS); do \
		echo "building $$app"; \
		go build -o $(BIN_DIR)/$$app ./cmd/$$app; \
	done

build-all: ## Cross-compile every app for linux, windows, darwin (amd64 + arm64)
	@for app in $(APPS); do \
		for os in linux windows darwin; do \
			for arch in amd64 arm64; do \
				ext=$$( [ $$os = windows ] && echo .exe || echo ); \
				echo "building $$app $$os/$$arch"; \
				GOOS=$$os GOARCH=$$arch go build -o $(BIN_DIR)/$$app-$$os-$$arch$$ext ./cmd/$$app; \
			done; \
		done; \
	done

clean: ## Remove build artifacts
	rm -rf $(BIN_DIR) coverage.out

oapicodegen: ## Generate OpenAPI server code
	bash scripts/oapicodegen.sh