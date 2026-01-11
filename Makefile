.PHONY: build test clean docker lint run help

# Variables
BINARY := meta-core
VERSION ?= 1.0.0
BUILD_DIR := bin
DOCKER_IMAGE := meta-core

# Go build flags
LDFLAGS := -ldflags="-s -w -X main.Version=$(VERSION)"
BUILD_FLAGS := CGO_ENABLED=0

# Default target
all: build

# Build the binary
build:
	@echo "Building $(BINARY)..."
	@mkdir -p $(BUILD_DIR)
	$(BUILD_FLAGS) go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY) ./cmd/meta-core

# Build for Linux (cross-compile)
build-linux:
	@echo "Building $(BINARY) for Linux..."
	@mkdir -p $(BUILD_DIR)
	$(BUILD_FLAGS) GOOS=linux GOARCH=amd64 go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY)-linux-amd64 ./cmd/meta-core

# Run tests
test:
	@echo "Running tests..."
	go test -v ./...

# Run tests with coverage
test-cover:
	@echo "Running tests with coverage..."
	go test -v -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report: coverage.html"

# Run linter
lint:
	@echo "Running linter..."
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run; \
	else \
		echo "golangci-lint not installed, running go vet instead"; \
		go vet ./...; \
	fi

# Format code
fmt:
	@echo "Formatting code..."
	go fmt ./...

# Tidy dependencies
tidy:
	@echo "Tidying dependencies..."
	go mod tidy

# Clean build artifacts
clean:
	@echo "Cleaning..."
	rm -rf $(BUILD_DIR)
	rm -f coverage.out coverage.html

# Build Docker image
docker:
	@echo "Building Docker image..."
	docker build -t $(DOCKER_IMAGE):$(VERSION) .
	docker tag $(DOCKER_IMAGE):$(VERSION) $(DOCKER_IMAGE):latest

# Run locally (for development)
run: build
	@echo "Running $(BINARY)..."
	META_CORE_PATH=/tmp/meta-core \
	FILES_PATH=/tmp/files \
	SERVICE_NAME=meta-core-dev \
	META_CORE_HTTP_PORT=9000 \
	./$(BUILD_DIR)/$(BINARY)

# Install to system
install: build
	@echo "Installing $(BINARY) to /usr/local/bin..."
	sudo cp $(BUILD_DIR)/$(BINARY) /usr/local/bin/

# Check if Go is installed
check:
	@echo "Go version:"
	@go version
	@echo ""
	@echo "Dependencies:"
	@go list -m all

# Help
help:
	@echo "Available targets:"
	@echo "  build       - Build the binary"
	@echo "  build-linux - Build for Linux (cross-compile)"
	@echo "  test        - Run tests"
	@echo "  test-cover  - Run tests with coverage"
	@echo "  lint        - Run linter"
	@echo "  fmt         - Format code"
	@echo "  tidy        - Tidy dependencies"
	@echo "  clean       - Clean build artifacts"
	@echo "  docker      - Build Docker image"
	@echo "  run         - Build and run locally"
	@echo "  install     - Install to /usr/local/bin"
	@echo "  check       - Check Go installation and dependencies"
	@echo "  help        - Show this help"
