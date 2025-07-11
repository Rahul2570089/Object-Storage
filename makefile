# Object Storage System Makefile

# Go parameters
GOCMD=go
GOBUILD=$(GOCMD) build
GOCLEAN=$(GOCMD) clean
GOTEST=$(GOCMD) test
GOGET=$(GOCMD) get
GOMOD=$(GOCMD) mod

# Binary names
SERVER_BINARY=storage-server
CLI_BINARY=storage-cli

# Build directory
BUILD_DIR=build

# Default target
.PHONY: all
all: build

# Build all binaries
.PHONY: build
build: clean
	@echo "Building binaries..."
	@mkdir -p $(BUILD_DIR)
	$(GOBUILD) -o $(BUILD_DIR)/$(SERVER_BINARY) -v ./cmd/server
	$(GOBUILD) -o $(BUILD_DIR)/$(CLI_BINARY) -v ./cmd/cli
	@chmod +x $(BUILD_DIR)/*
	@echo "Build complete. Binaries in $(BUILD_DIR)/"

# Build server only
.PHONY: server
server:
	@echo "Building server..."
	@mkdir -p $(BUILD_DIR)
	$(GOBUILD) -o $(BUILD_DIR)/$(SERVER_BINARY) -v ./cmd/server
	@chmod +x $(BUILD_DIR)/$(SERVER_BINARY)

# Build CLI client only
.PHONY: cli
cli:
	@echo "Building CLI client..."
	@mkdir -p $(BUILD_DIR)
	$(GOBUILD) -o $(BUILD_DIR)/$(CLI_BINARY) -v ./cmd/cli
	@chmod +x $(BUILD_DIR)/$(CLI_BINARY)

# Run server
.PHONY: run-server
run-server:
	@echo "Starting storage server on :8080..."
	$(GOCMD) run ./cmd/server

# Run CLI with help
.PHONY: run-cli
run-cli:
	$(GOCMD) run ./cmd/cli help

# Clean build artifacts
.PHONY: clean
clean:
	@echo "Cleaning..."
	$(GOCLEAN)
	rm -rf $(BUILD_DIR)
	rm -rf ./storage

# Run tests
.PHONY: test
test:
	@echo "Running tests..."
	$(GOTEST) -v ./...

# Run tests with coverage
.PHONY: test-coverage
test-coverage:
	@echo "Running tests with coverage..."
	$(GOTEST) -v -coverprofile=coverage.out ./...
	$(GOCMD) tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report generated: coverage.html"

# Initialize go module
.PHONY: init
init:
	$(GOMOD) init storage-system
	$(GOMOD) tidy

# Download dependencies
.PHONY: deps
deps:
	@echo "Downloading dependencies..."
	$(GOMOD) download
	$(GOMOD) tidy

# Format code
.PHONY: fmt
fmt:
	@echo "Formatting code..."
	$(GOCMD) fmt ./...

# Lint code (requires golangci-lint)
.PHONY: lint
lint:
	@echo "Linting code..."
	golangci-lint run

# Install binaries to GOPATH/bin
.PHONY: install
install:
	@echo "Installing binaries..."
	$(GOCMD) install ./cmd/server
	$(GOCMD) install ./cmd/cli

# Updated quick start
.PHONY: quick-start
quick-start: build
	@echo "Quick start guide:"
	@echo "1. Start the server: make run-server"
	@echo "2. In another terminal, use the CLI client:"
	@echo "   ./$(BUILD_DIR)/$(CLI_BINARY) mb my-bucket"
	@echo "   echo 'Hello World' > test.txt"
	@echo "   ./$(BUILD_DIR)/$(CLI_BINARY) cp test.txt my-bucket/test.txt"
	@echo "   ./$(BUILD_DIR)/$(CLI_BINARY) ls my-bucket"
	@echo "   ./$(BUILD_DIR)/$(CLI_BINARY) cat my-bucket/test.txt"

# Updated demo - removes references to separate client
.PHONY: demo
demo: build
	@echo "Running demo..."
	@echo "1. Starting server in background..."
	@./$(BUILD_DIR)/$(SERVER_BINARY) &
	@SERVER_PID=$$!; \
	sleep 2; \
	echo "2. Creating bucket..."; \
	./$(BUILD_DIR)/$(CLI_BINARY) mb demo-bucket; \
	echo "3. Creating test file..."; \
	echo "Hello from Object Storage!" > test-file.txt; \
	echo "4. Uploading file..."; \
	./$(BUILD_DIR)/$(CLI_BINARY) cp test-file.txt demo-bucket/hello.txt; \
	echo "5. Listing buckets..."; \
	./$(BUILD_DIR)/$(CLI_BINARY) ls; \
	echo "6. Listing objects..."; \
	./$(BUILD_DIR)/$(CLI_BINARY) ls demo-bucket; \
	echo "7. Downloading file..."; \
	./$(BUILD_DIR)/$(CLI_BINARY) cp demo-bucket/hello.txt downloaded.txt; \
	echo "8. Viewing file content..."; \
	./$(BUILD_DIR)/$(CLI_BINARY) cat demo-bucket/hello.txt; \
	echo; \
	echo "9. Cleaning up..."; \
	rm -f test-file.txt downloaded.txt; \
	kill $$SERVER_PID; \
	echo "Demo complete!"

# Help
.PHONY: help
help:
	@echo "Object Storage System - Available Make Targets:"
	@echo ""
	@echo "Building:"
	@echo "  build          Build all binaries"
	@echo "  server         Build server only"
	@echo "  cli            Build CLI client only"
	@echo "  clean          Clean build artifacts"
	@echo ""
	@echo "Running:"
	@echo "  run-server     Start the storage server"
	@echo "  run-cli        Run CLI client with help"
	@echo "  demo           Run complete demo"
	@echo ""
	@echo "Development:"
	@echo "  init           Initialize Go module"
	@echo "  deps           Download dependencies"
	@echo "  fmt            Format code"
	@echo "  lint           Lint code (requires golangci-lint)"
	@echo "  test           Run tests"
	@echo "  test-coverage  Run tests with coverage"
	@echo "  dev-setup      Setup development environment"
	@echo ""
	@echo "Installation:"
	@echo "  install        Install binaries to GOPATH/bin"
	@echo ""
	@echo "Getting Started:"
	@echo "  quick-start    Show quick start guide"
	@echo "  help           Show this help message"