# Repository Guidelines

## Project Structure & Module Organization

This repository is currently specification- and prototype-focused:

- `docs/SPEC.md` defines requirements and contracts.
- `docs/DESIGN.md` defines architecture and implementation boundaries.
- `docs/PLAN.md` is reserved for larger implementation plans; smaller plans
  belong in `docs/plans/`.
- `docs/protocols/` contains evaluation and operational protocols.
- `prototypes/model_selection/` contains InsightFace benchmarking tools.
- `prototypes/model_settings_review/` contains the review web prototype and
  its tests.
- `docs/` may contain private planning material, but private images,
  embeddings, databases, and generated benchmark output must not be committed.

The planned production implementation will use Go for application services
and a Python worker for InsightFace operations.

## Build, Test, and Development Commands

There is no production build system yet. For the model-selection prototype:

```sh
cd prototypes/model_selection
./setup.sh
source .venv/bin/activate
python src/check_runtime.py --load-model
```

Run a recursive benchmark with `python src/benchmark.py <photo-directory>`.
The review prototype can be run with its documented `review_tool.py serve`
command. Generated results should remain under prototype result directories.

## Coding Style & Naming Conventions

Use four spaces for Python indentation, type annotations for new Python code,
and descriptive `snake_case` names. Keep worker stdout reserved for protocol
messages; send diagnostics to stderr. Go code should follow standard `gofmt`
formatting and idiomatic package naming. Use Markdown headings and concise
prose in documentation. Prefer ISO-8601 timestamps and explicit versioned
formats for persisted or exchanged data.

## Testing Guidelines

Prototype tests use Python’s `unittest` framework. Run them with:

```sh
python -m unittest discover -s prototypes/model_settings_review/tests
```

Add regression tests for protocol validation, database/state transitions,
filtering, and review behavior. Do not use private photos or embeddings as
committed fixtures.

## Commit & Pull Request Guidelines

Use short, imperative commit subjects consistent with the existing history,
for example `Update SPEC after reviewing open decisions`. Keep commits focused
and explain important architectural or data-model changes in the body.

Pull requests should describe the behavior changed, identify relevant SPEC or
DESIGN sections, include test commands and results, and include screenshots
when changing the review UI. Call out generated files, migrations, privacy
implications, and any required setup or model downloads.

## Security & Configuration

Treat original photos, embeddings, databases, backups, and exports as private
data. Do not commit them. Keep model/dependency versions and checksums
recorded, and verify that local configuration paths do not expose sensitive
data in logs or generated reports.
