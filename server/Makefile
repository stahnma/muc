.PHONY: all build clean test fmt tidy run help

# Build configuration
NAME := muc-server
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
	@echo "Available server targets:"
	@echo "  all          - Build server (default)"
	@echo "  build        - Build server binary"
	@echo "  clean        - Clean build artifacts"
	@echo "  test         - Run tests"
	@echo "  fmt          - Format code"
	@echo "  tidy         - Tidy Go modules"
	@echo "  run          - Run the server"

# Build targets
build: fmt tidy
	@echo "Building server binary..."
	$(GO) build $(GOFLAGS) $(LDFLAGS) -o $(NAME) .

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

# Run target
run: build
	@echo "Starting server..."
	./$(NAME)

# Cleanup
clean:
	@echo "Cleaning build artifacts..."
	rm -f $(NAME)
