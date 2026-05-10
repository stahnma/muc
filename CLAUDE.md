# CLAUDE.md

## Build & Test

Use the root Makefile for all workflow operations:

- `make build` — Build client and server (runs fmt and tidy first)
- `make test` — Run all tests
- `make fmt` — Format Go code
- `make lint` — Run linters
- `make clean` — Clean build artifacts
- `make tidy` — Tidy Go modules

Individual components can be built with `make client` or `make server`.

CGO is disabled by default (`CGO_ENABLED=0`).
