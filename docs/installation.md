# Installation and setup

The default installation uses system Python 3.11 plus a repository-local
virtual environment. It does not install a bundled Python distribution.

From the repository root:

```sh
./scripts/setup_worker.sh
source .venv/bin/activate
```

The script installs the pinned worker dependencies, downloads `buffalo_l` into
`runtime/models`, writes a SHA-256 manifest, and performs an offline startup
validation. `--clean` removes and recreates the virtual environment. If setup
is interrupted, rerunning the script removes the temporary `.installing.*`
directory and resumes from the last completed stage. The direct dependency
pins are in `worker/requirements.lock`; the completed environment freeze is
written to `.venv/requirements.lock.txt`. Supported Python/provider profiles
are recorded in `worker/platforms.json`.

Useful overrides include `PYTHON_BIN`, `FACE_ME_BRO_VENV`,
`FACE_ME_BRO_MODEL_ROOT`, and `FACE_ME_BRO_MODEL`.

Validate an already-installed runtime without network access:

```sh
.venv/bin/python scripts/setup_models.py --model buffalo_l \
  --root runtime/models --offline
```

Run the application with an alternate configuration using `--config`. The
configuration may set `runtime_data_root`, database, model, log, temporary,
export, and backup paths. Register a pipeline, then start the worker:

```sh
face-me-bro pipeline register --model-family buffalo_l
face-me-bro worker --pipeline-id <pipeline-id>
```

An optional Docker worker configuration is documented in `docker/README.md`.

## Configuration selection

The application accepts strict JSON and JSONC configuration files. Select a
file or directory with `--config PATH`, or set `FACE_ME_PRO_CONFIG`. A
directory uses `config.json`. Supplying both is allowed only when both values
resolve to the same file; conflicting values are rejected before the database
is opened.
