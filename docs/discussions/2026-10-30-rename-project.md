# Project Rename: Who That Face

This document inventories the repository references that would need to change
if the project is renamed from Face-Me-Bro to Who That Face.

The proposed public identifiers are:

| Existing | Proposed |
| --- | --- |
| Display name: `Face-Me-Bro` / `face_me_bro` | `Who That Face` |
| CLI command: `face-me-bro` | `whothatface` |
| Benchmark command: `face-me-bro-benchmark` | `whothatface-benchmark` |
| Go module/import path: `github.com/pjg/face_me_bro` | `github.com/weisnobody/WhoThatFace` |
| Environment prefix: `FACE_ME_BRO_*` | `WTFACE_*` |
| Config environment variable: `FACE_ME_PRO_CONFIG` | `WTFACE_CONFIG` |
| Runtime data directory: `face_me_bro` | `whothatface` |
| Default database: `face_me_bro.sqlite3` | `whothatface.sqlite3` |

This assumes lowercase `whothatface` for filesystem paths and database names,
matching the command name.

## Files and directories to rename

- `cmd/face-me-bro/` → `cmd/whothatface/`
  - Includes `main.go`, `main_test.go`, `charts.go`, and `charts_test.go`.
- `cmd/face-me-bro-benchmark/` → `cmd/whothatface-benchmark/`
  - Includes `main.go`.
- Local repository directory:
  - `/Users/pjg/Projects/face_me_bro` → something like
    `/Users/pjg/Projects/WhoThatFace`.
- GitHub repository:
  - `github.com/pjg/face_me_bro` → `github.com/weisnobody/WhoThatFace`.

## Go module and imports

`go.mod` currently declares:

```go
module github.com/pjg/face_me_bro
```

It should become:

```go
module github.com/weisnobody/WhoThatFace
```

All internal imports using `github.com/pjg/face_me_bro/...` must be updated.
These occur throughout:

- `cmd/face-me-bro/`
- `cmd/face-me-bro-benchmark/`
- `internal/analysis/`
- `internal/app/`
- `internal/audit/`
- `internal/auth/`
- `internal/candidates/`
- `internal/clustering/`
- `internal/config/`
- `internal/db/`
- `internal/jobs/`
- `internal/media/`
- `internal/perf/`
- `internal/recognition/`
- `internal/representative/`
- `internal/review/`
- `internal/search/`
- `internal/web/`
- `internal/worker/`
- `schemas/`

This is content-only after the command directories are renamed.

## CLI implementation and tests

`cmd/face-me-bro/main.go` contains the main user-facing references:

- flag set name: `face-me-bro`
- error prefix: `face-me-bro:`
- usage text: `Usage: face-me-bro ...`
- help text and command descriptions
- exported metadata field: `"Application": "face-me-bro"`

These should become `whothatface`.

`cmd/face-me-bro/main_test.go` contains expected output strings such as:

```text
Usage: face-me-bro
Usage: face-me-bro face
Usage: face-me-bro search
```

Those tests should be updated to expect `whothatface`.

## Environment variables

Current variables include:

```text
FACE_ME_PRO_CONFIG
FACE_ME_BRO_VENV
FACE_ME_BRO_MODEL_ROOT
FACE_ME_BRO_MODEL
FACE_ME_BRO_ACTIVITY
FACE_ME_BRO_THERMAL_PRESSURE
FACE_ME_BRO_POWER_STATE
```

Proposed replacements:

```text
WTFACE_CONFIG
WTFACE_VENV
WTFACE_MODEL_ROOT
WTFACE_MODEL
WTFACE_ACTIVITY
WTFACE_THERMAL_PRESSURE
WTFACE_POWER_STATE
```

References occur in:

- `internal/config/config.go`
- `internal/jobs/policy.go`
- `internal/setup/check.go`
- `scripts/setup_worker.sh`
- `scripts/test_setup_worker.py`
- `worker/test_insightface_worker.py`
- `README.md`
- `installation.md`
- `usage.md`
- `production_operations.md`
- `docker/Dockerfile`
- `docs/plans/2026-10-11-spec-implemention-gaps.md`

There is an existing inconsistency: configuration uses `FACE_ME_PRO_CONFIG`,
while most other variables use `FACE_ME_BRO`. Since backward compatibility is
not needed, this is a good opportunity to standardize it as `WTFACE_CONFIG`.

## Runtime paths and database names

These should be updated in code, tests, examples, and documentation:

- `internal/config/config.go`
  - default macOS/Linux/Windows data directories
  - default database name
- `internal/config/config_test.go`
  - expected runtime directory basename
- `internal/app/app_test.go`
  - expected database path
- `internal/db/db_test.go`
  - temporary database filename
- `internal/perf/benchmark.go`
  - temporary performance database prefix
- `config.example.jsonc`
- `docs/SPEC.md`
- `docs/DESIGN.md`
- `docs/discussions/2026-10-08-cross-platform-considerations.md`
- `docs/plans/2026-10-09-implementation-initial.md`
- `schemas/migrations/001_initial.sql`
  - comment only; no schema/table names need renaming

The database schema’s generic names such as `faces`, `face_id`, and
`face_index` should remain unchanged. They describe domain concepts, not the
product name.

## Documentation and operational examples

The following documents contain build commands, executable names, or product
references that should be updated:

- `README.md`
- `installation.md`
- `usage.md`
- `production_operations.md`
- `docker/README.md`
- `docs/DESIGN.md`
- `docs/SPEC.md`
- `docs/possible_face_candidates.md`
- `docs/protocols/performance_evaluation.md`
- `docs/discussions/2026-10-08-cross-platform-considerations.md`
- `docs/plans/2026-10-09-implementation-initial.md`
- `docs/plans/2026-10-11-spec-implemention-gaps.md`
- `docs/plans/2026-10-13-possible-face-candidates.md`
- `docs/plans/2026-10-30-update-documentation.md`

Examples such as:

```sh
go build -o bin/face-me-bro ./cmd/face-me-bro
./bin/face-me-bro serve
go run ./cmd/face-me-bro-benchmark
```

should become:

```sh
go build -o bin/whothatface ./cmd/whothatface
./bin/whothatface serve
go run ./cmd/whothatface-benchmark
```

## Docker and worker references

Update:

- `docker/Dockerfile`
  - `/opt/face-me-bro` → `/opt/whothatface`
  - `FACE_ME_BRO_MODEL_ROOT` → `WTFACE_MODEL_ROOT`
- `docker/README.md`
  - image tag `face-me-bro-worker` → likely `whothatface-worker`
- `worker/insightface_worker.py`
  - description `Face-Me-Bro InsightFace NDJSON worker` →
    `Who That Face InsightFace NDJSON worker`

## Web UI and service metadata

Update in:

- `internal/web/server.go`
  - navigation label `Face-Me-Bro`
  - health/service JSON value `"face-me-bro"`
- `internal/web/server_test.go`
  - expected rendered label `Face-Me-Bro`
- `internal/db/db.go`
  - restore validation error mentioning `face-me-bro`
- `internal/perf/benchmark.go`
  - temporary filename prefix

Suggested replacements:

```text
Face-Me-Bro → Who That Face
face-me-bro → whothatface
```

## Files to leave alone

Do not mechanically replace every occurrence of “face.” These are domain
terms and should remain:

- `faces`
- `face_id`
- `face_index`
- `face-analysis`
- `face recognition`
- `InsightFace`
- `possible_face_candidates`
- URLs such as `/review/faces`
- schema and migration table names

The untracked `tmp/` directory contains private/generated SQLite files and
config data with old names. It should not be treated as source code or
committed. Its contents can be regenerated or renamed separately if desired.

The repository’s `.git/config` and `.git/logs/` also contain the old remote
path. Those are Git metadata rather than project source; the remote should be
changed with Git after the GitHub repository is renamed or recreated.
