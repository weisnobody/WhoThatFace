# Installation and setup

The default installation uses system Python 3.11 plus a repository-local
virtual environment. It does not install a bundled Python distribution.

From the repository root:

```sh
./scripts/setup_worker.sh
source .venv/bin/activate
```

The script installs the pinned worker dependencies (including Pillow and
`pillow-heif`), downloads `buffalo_l` into
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
configuration may set `runtime_data_root`, database, model, Python, provider,
serve address, log, temporary, export, and backup paths. `python_path` defaults
to the virtual environment under `runtime_data_root`, `serve_address` defaults
to `127.0.0.1:8080`, and `provider` defaults to `CPUExecutionProvider`.
Register a pipeline, then start the worker:

```sh
APP="$PWD/bin/face-me-bro"
CONFIG="$PWD/config.json"

"$APP" --config "$CONFIG" setup check
"$APP" --config "$CONFIG" pipeline register --model-family buffalo_l
PIPELINE_ID="<pipeline-id printed by pipeline register>"
"$APP" --config "$CONFIG" worker \
  --pipeline-id "$PIPELINE_ID" \
  --script "$PWD/worker/insightface_worker.py"
```

Create `config.json` before running these commands; the complete example,
including database and runtime paths, is in [`docs/usage.md`](usage.md).
Keep the worker running in its own terminal while scans and review-triggered
jobs are processed.

For the full first-run workflow, including collection setup, review, backups,
and maintenance, see [`docs/usage.md`](usage.md).

An optional Docker worker configuration is documented in `docker/README.md`.

## Configuration selection

The application accepts strict JSON and JSONC configuration files. Select a
file or directory with `--config PATH`, or set `FACE_ME_PRO_CONFIG`. A
directory uses `config.json`. Supplying both is allowed only when both values
resolve to the same file; conflicting values are rejected before the database
is opened.
