# CLAUDE.md

## Toolchain

This project ships a [flox](https://flox.dev) environment (`.flox/`) that provides
`go`, `gnumake`, and `goreleaser`. None of these are on the bare system `PATH`, so
both `make` and `go` must be run through flox:

- `flox activate -- make build` — run a Makefile target (preferred)
- `flox activate -- go version` — run a one-off Go command
- `flox activate` — enter a shell with the toolchain on `PATH`, then use `make`/`go` directly

## Build & Test

Use the root Makefile for all workflow operations (all via `flox activate -- ...`):

- `make build` — Build client and server (runs fmt and tidy first)
- `make test` — Run all tests
- `make fmt` — Format Go code
- `make lint` — Run linters
- `make clean` — Clean build artifacts
- `make tidy` — Tidy Go modules

Individual components can be built with `make client` or `make server`.

CGO is disabled by default (`CGO_ENABLED=0`).
