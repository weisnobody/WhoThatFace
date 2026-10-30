# Rename Project to Who That Face

## Summary

Rename the project’s product, CLI, Go module, environment variables, runtime
paths, Docker identifiers, and documentation references from Face-Me-Bro to
Who That Face.

Use these final identifiers:

- Display name: `Who That Face`
- CLI: `whothatface`
- Benchmark CLI: `whothatface-benchmark`
- GitHub repository: `github.com/weisnobody/WhoThatFace`
- Go module/import path: `github.com/weisnobody/whothatface`
- Environment prefix: `WTFACE`
- Config variable: `WTFACE_CONFIG`
- Runtime data directory: `whothatface`
- Default database: `whothatface.sqlite3`

No backward-compatibility aliases or data migration are required.

## Implementation changes

- Rename `cmd/face-me-bro/` to `cmd/whothatface/`.
- Rename `cmd/face-me-bro-benchmark/` to `cmd/whothatface-benchmark/`.
- Change `go.mod` to `github.com/weisnobody/whothatface` and update every
  internal Go import using the old module path.
- Replace product-facing CLI strings in the main command, benchmark code,
  tests, error messages, help output, exported metadata, health responses, and
  web navigation.
- Replace environment variables consistently:
  - `FACE_ME_PRO_CONFIG` → `WTFACE_CONFIG`
  - `FACE_ME_BRO_VENV` → `WTFACE_VENV`
  - `FACE_ME_BRO_MODEL_ROOT` → `WTFACE_MODEL_ROOT`
  - `FACE_ME_BRO_MODEL` → `WTFACE_MODEL`
  - `FACE_ME_BRO_ACTIVITY` → `WTFACE_ACTIVITY`
  - `FACE_ME_BRO_THERMAL_PRESSURE` → `WTFACE_THERMAL_PRESSURE`
  - `FACE_ME_BRO_POWER_STATE` → `WTFACE_POWER_STATE`
- Update default runtime directories, database filenames, temporary benchmark
  filenames, configuration examples, and related tests to use lowercase
  `whothatface`.
- Update Docker paths, image tags, worker descriptions, and Docker environment
  variables.
- Update active documentation, dated plans, discussions, protocols, examples,
  and operational commands so executable references use the new identifiers.
- Leave domain concepts unchanged, including `face`, `faces`, `face_id`,
  `face_index`, `InsightFace`, schema table names, migration names, and
  face-related URLs.
- Do not alter private/generated files under the untracked `tmp/` directory.
- Do not modify `.git/config` or Git history as part of the source refactor.

## Tests and verification

Run formatting and the complete Go test suite:

```sh
gofmt -w cmd/whothatface cmd/whothatface-benchmark internal schemas
go test ./...
```

Run the Python tests that exercise worker and setup environment variables:

```sh
python -m unittest worker.test_insightface_worker scripts.test_setup_models scripts.test_setup_worker
python -m unittest discover -s prototypes/model_settings_review/tests
```

Verify that both renamed binaries build, CLI help and service metadata use the
new names, `WTFACE_*` variables are read correctly, default runtime paths use
`whothatface`, Docker tests reference the new paths, and a scoped search finds
no stale executable, module, environment, runtime-path, or display-name
references outside the rename inventory, this plan, `.git/`, and `tmp/`.
