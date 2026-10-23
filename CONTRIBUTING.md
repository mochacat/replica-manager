# Contributing

Thanks for taking an interest! This is a portfolio project, so contributions
are limited to bug fixes and documentation improvements.

## Prerequisites

- Go 1.26+
- make
- Docker, KIND, helm
- protoc (for proto changes)

## Getting started

```bash
make kind-up
make deploy
```

## Running tests

```bash
make test                # unit tests
make integration-test    # integration tests (creates KIND cluster, runs, cleans up)
```

## Code style

- Run `go vet ./...` before committing
- Tests use table-driven patterns with `testing.T`
- Integration tests use the `testing` package with `t.Run` subtests

## Pull requests

1. Fork the repo
2. Create a feature branch
3. Run tests
4. Open a PR with a clear description of the change