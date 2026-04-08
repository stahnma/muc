.PHONY: all build clean test fmt tidy linux help

# Build configuration
NAME := client
GO := go
CGO_ENABLED ?= 0
export CGO_ENABLED
GOFLAGS := -v
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS := -ldflags "-X main.Version=$(VERSION)"

# Default target
all: build

# Help target
help:
	@echo "Available client targets:"
	@echo "  all          - Build client (default)"
	@echo "  build        - Build client binary"
	@echo "  clean        - Clean build artifacts"
	@echo "  test         - Run tests"
	@echo "  fmt          - Format code"
	@echo "  tidy         - Tidy Go modules"
	@echo "  linux        - Build Linux binaries"

# Build targets
build: fmt tidy
	@echo "Building client binary..."
	$(GO) build $(GOFLAGS) $(LDFLAGS) -o $(NAME) .

# Cross-compilation for Linux
linux: fmt
	@echo "Building Linux binaries..."
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build $(GOFLAGS) $(LDFLAGS) -o $(NAME).linux.amd64 .
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 $(GO) build $(GOFLAGS) $(LDFLAGS) -o $(NAME).linux.arm64 .
	CGO_ENABLED=0 GOOS=linux GOARCH=arm $(GO) build $(GOFLAGS) $(LDFLAGS) -o $(NAME).linux.arm .

# Development targets
fmt:
	@echo "Formatting code..."
	$(GO) fmt ./...

test:
	@echo "Running tests..."
	$(GO) test -v ./...

tidy:
	@echo "Tidying modules..."
	$(GO) mod tidy

# Cleanup
clean:
	@echo "Cleaning build artifacts..."
	rm -f $(NAME) $(NAME).linux.*
