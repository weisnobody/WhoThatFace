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

The default environment includes Pillow and `pillow-heif`, so the supported
HEIF/HEIC path works for worker analysis and representative-image generation.

### Additional Pillow plugins

Pillow plugins can add decoders for formats that are not included in the
default environment. For a local experiment, activate the worker environment
and install the plugin into it:

```sh
source .venv/bin/activate
python -m pip install <plugin-package>
```

For a reproducible setup, add a pinned package to both
`worker/requirements.in` and `worker/requirements.lock`, then rerun
`./scripts/setup_worker.sh`. If the plugin requires explicit registration,
register it in the worker before the first `Image.open` call. Adding a Pillow
plugin alone does not expand the application’s accepted formats: the Go media
scanner, metadata/dimension handling, worker decoding, representative-image
cropper, and tests must all support the extension. The current end-to-end
allowlist is JPEG/JPG, PNG, HEIF/HEIC, TIFF/TIF, and WebP.

[`config.example.jsonc`](config.example.jsonc) lists every configuration
parameter, its purpose, and its default value. It uses JSONC comments for
documentation; the application configuration file itself must be strict JSON.
Copy the uncommented settings you need into `config.json`, use the
`--config PATH` option, or set `FACE_ME_PRO_CONFIG` to an alternate JSON or
JSONC file (or a directory containing `config.json`). If both the environment
variable and `--config` are supplied, they must resolve to the same file.

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

For a complete first-run workflow, continue with
[`docs/usage.md`](docs/usage.md). The shorter installation notes are in
[`docs/installation.md`](docs/installation.md), and
[`config.example.jsonc`](config.example.jsonc) documents every configuration
setting.

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
- [`docs/installation.md`](docs/installation.md) — worker setup and
  configuration selection.
- [`docs/usage.md`](docs/usage.md) — end-to-end collection and review workflow.
- [`config.example.jsonc`](config.example.jsonc) — documented configuration
  settings and defaults.
- [`docs/plans/`](docs/plans/) — implementation and feature plans.
- [`docs/protocols/performance_evaluation.md`](docs/protocols/performance_evaluation.md)
  — benchmark procedure.
