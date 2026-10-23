# Kubernetes Replica Manager — Project Guidance

This is a public portfolio project demonstrating end-to-end infrastructure
engineering: a gRPC control plane for declarative Kubernetes scaling with
controller/reconciliation patterns, cached gRPC APIs, mTLS, Helm packaging,
and automated dev workflows.

## Project setup

- **Cluster:** KIND (Kubernetes IN Docker)
- **Language:** Go
- **Build:** Make targets (see the Makefile and README)

## Development

For prerequisites, running tests, code style, and the pull request workflow,
see [CONTRIBUTING.md](CONTRIBUTING.md).

Key commands:

```bash
make test                # unit tests
make integration-test    # integration tests (KIND cluster)
make build               # build binary + Docker image
```

## Design

The full architecture and design decisions are in [DESIGN.md](DESIGN.md).
Implementation is tracked in [TASKS.md](TASKS.md).