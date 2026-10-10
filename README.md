# face_me_bro

Local facial recognition and identification with a Go application and an
InsightFace Python worker.

## Requirements

- Go toolchain supported by the repository's `go.mod`.
- System Python 3.11. The project creates a local virtual environment; it does
  not bundle or manage a Python distribution.
- Network access during first-time setup to download Python packages and the
  configured InsightFace model. Runtime processing works offline afterward.

## Setup

### Python Worker

From the repository root:

```sh
./scripts/setup_worker.sh
source .venv/bin/activate
```

This installs the pinned worker dependencies, downloads and verifies the
`buffalo_l` model, and records the complete environment freeze. See
[`docs/installation.md`](docs/installation.md) for configuration overrides,
offline validation, and the optional Docker worker.

[`config.example.jsonc`](config.example.jsonc) lists every configuration
parameter, its purpose, and its default value. It uses JSONC comments for
documentation; the application configuration file itself must be strict JSON.
Copy the uncommented settings you need into `config.json`, or use the
`--config PATH` option with an alternate file or directory.

### Go Application

The Go toolchain version must satisfy the `go` version in `go.mod`. From the
repository root:

```sh
go version
go mod download
mkdir -p bin
go build -o bin/face-me-bro ./cmd/face-me-bro
go build -o bin/face-me-bro-benchmark ./cmd/face-me-bro-benchmark
```

The first build produces the main CLI. The second produces the optional
performance benchmark executable. The binaries can be run directly, for
example `./bin/face-me-bro help`.

## Run and test

Check the configured runtime:

```sh
go run ./cmd/face-me-bro setup check
```

Start the local review service:

```sh
go run ./cmd/face-me-bro serve
```

Run the scale/performance harness:

```sh
go run ./cmd/face-me-bro-benchmark --output performance.json
```

Run all Go tests and static checks:

```sh
go test ./...
go vet ./...
```

The worker communicates with the Go application through the documented NDJSON
protocol on stdin/stdout. Use `--config PATH` to select an alternate
configuration file or directory and runtime data root.

## Documentation

- [`docs/SPEC.md`](docs/SPEC.md) — requirements and acceptance criteria.
- [`docs/DESIGN.md`](docs/DESIGN.md) — architecture and boundaries.
- [`docs/usage.md`](docs/usage.md) — end-to-end collection and review workflow.
- [`docs/plans/`](docs/plans/) — implementation and feature plans.
- [`docs/protocols/performance_evaluation.md`](docs/protocols/performance_evaluation.md)
  — benchmark procedure.
