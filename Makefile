.PHONY: all build clean test fmt lint install client server linux-all help

# Project configuration
NAME := muc
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
GOOS ?= $(shell go env GOOS)
GOARCH ?= $(shell go env GOARCH)

# Build configuration
GO := go
CGO_ENABLED ?= 0
export CGO_ENABLED
GOFLAGS :=
LDFLAGS := -ldflags "-X main.Version=$(VERSION)"

# Default target
all: build

# Help target
help:
	@echo "Available targets:"
	@echo "  all          - Build all components (default)"
	@echo "  build        - Build client and server"
	@echo "  clean        - Clean all build artifacts"
	@echo "  test         - Run all tests"
	@echo "  fmt          - Format Go code"
	@echo "  lint         - Run linters"
	@echo "  install      - Install binaries"
	@echo "  linux-all    - Build for all Linux architectures"
	@echo "  client       - Build only the client"
	@echo "  server       - Build only the server"

# Build targets
build: fmt tidy client server

client:
	@echo "Building client..."
	$(MAKE) -C client build

server:
	@echo "Building server..."
	$(MAKE) -C server build

# Cross-compilation targets
linux-all:
	@echo "Building for all Linux architectures..."
	$(MAKE) -C client linux
	GOOS=linux GOARCH=amd64 $(MAKE) -C server build
	GOOS=linux GOARCH=arm64 $(MAKE) -C server build

# Development targets
fmt:
	@echo "Formatting code..."
	$(GO) fmt ./...
	$(MAKE) -C client fmt
	$(MAKE) -C server fmt

lint:
	@echo "Running linters..."
	$(GO) vet ./...
	@if command -v golangci-lint >/dev/null; then \
		golangci-lint run ./...; \
	else \
		echo "golangci-lint not installed. Run: go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest"; \
	fi

test:
	@echo "Running tests..."
	$(GO) test -v ./...
	$(MAKE) -C client test
	$(MAKE) -C server test

tidy:
	@echo "Tidying modules..."
	$(GO) mod tidy
	$(MAKE) -C client tidy
	$(MAKE) -C server tidy

# Cleanup
clean:
	@echo "Cleaning build artifacts..."
	$(MAKE) -C client clean
	$(MAKE) -C server clean
	rm -f $(NAME)
	rm -rf dist/

# Installation
install: build
	@echo "Installing binaries..."
	install -d $(DESTDIR)/usr/local/bin
	install -p -m755 client/muc-client $(DESTDIR)/usr/local/bin/$(NAME)-client
	install -p -m755 server/muc-server $(DESTDIR)/usr/local/bin/$(NAME)-server

run: fmt
	go run *.go

json:
	cat message | jq > m ; mv m message

iter:
	go build -o muc-client client/main.go

rerun:
	flox services stop || true
	rm -f server/systems.db
	$(MAKE) -C client linux
	$(MAKE) -C server build
	cp -pr client/muc-client.linux.amd64 ~
	scp client/muc-client.linux.amd64 stahnma@rog:muc-client.linux.amd64

