# Usage

This guide describes the current end-to-end workflow: install the Python
worker, configure the application, register a model pipeline, load a photo
collection, process it, and review the results.

The commands below run from the repository root. The example keeps all local
runtime data under `runtime/`; choose another private location if required.

## 1. Install and configure the runtime

Use Python 3.11. Put the virtual environment and model files where the
application configuration will find them:

```sh
export WTFACE_VENV="$PWD/runtime/venv"
export WTFACE_MODEL_ROOT="$PWD/runtime/models"
export WTFACE_MODEL=buffalo_l

./scripts/setup_worker.sh
go mod download
mkdir -p bin
go build -o bin/whothatface ./cmd/whothatface
```

Create a strict JSON configuration file. Relative paths are resolved below
`runtime_data_root`; the model root must match the setup script's model root.

```sh
cat > config.json <<EOF
{
  "runtime_data_root": "$PWD/runtime",
  "model_root": "$PWD/runtime/models",
  "python_path": "$PWD/runtime/venv/bin/python",
  "provider": "CPUExecutionProvider",
  "serve_address": "127.0.0.1:8080",
  "neighbor_search_method": "exact"
}
EOF

APP="$PWD/bin/whothatface"
CONFIG="$PWD/config.json"

"$APP" --config "$CONFIG" status
"$APP" --config "$CONFIG" setup check
```

`setup check` validates Python, the configured virtual environment, worker
dependencies, the model directory, and its checksum manifest. The setup
script downloads `buffalo_l` to
`runtime/models/models/buffalo_l/` and writes `checksums.json` there. Keep
the model directory, database, exports, backups, and photos private.

The CLI accepts `--config PATH` on every command. It also accepts
`WTFACE_CONFIG`; do not provide conflicting values.

Inspect the resolved configuration and change the application-wide
neighbor-search default when needed:

```sh
"$APP" --config "$CONFIG" config show
"$APP" --config "$CONFIG" config set-search --method exact
```

Use `status` to see the selected database, model root, Python executable,
provider, pipeline, and search method. A collection can override this default
with `collection set-search` below.

## 2. Register the model pipeline

Register the exact model and execution provider that the worker will use. The
`--provider` default comes from the machine-specific `provider` configuration
setting, and can still be overridden for an individual pipeline. The pipeline
record is the contract checked during worker startup.

```sh
CHECKSUMS="$PWD/runtime/models/models/buffalo_l/checksums.json"
PIPELINE_ID=$("$APP" --config "$CONFIG" pipeline register \
  --model-family buffalo_l \
  --model-version 2.0.0 \
  --model-checksums-json "$(<"$CHECKSUMS")")

"$APP" --config "$CONFIG" pipeline get --pipeline-id "$PIPELINE_ID"
```

The register command also accepts explicit detector and preprocessing JSON
with `--detector-config-json` and `--preprocessing-config-json`. Use
`pipeline list` to see existing pipeline IDs; a worker must be started with a
registered pipeline ID.

## 3. Add and scan a collection

A collection is a directory tree. Add it once and retain the printed UUID:

```sh
PHOTO_ROOT="/path/to/Family Photos"
COLLECTION_ID=$("$APP" --config "$CONFIG" collection add \
  --name "Family Photos" --root "$PHOTO_ROOT")

"$APP" --config "$CONFIG" collection get --collection-id "$COLLECTION_ID"
"$APP" --config "$CONFIG" collection set-search \
  --collection-id "$COLLECTION_ID" --method exact
"$APP" --config "$CONFIG" collection set-enabled \
  --collection-id "$COLLECTION_ID" --enabled=true
```

`exact` is the correctness/reference search path. `ann` selects the
approximate-search prototype. The method can be changed later with the same
`collection set-search` command.

Disable a collection without deleting it, or remove it after confirming the
destructive operation:

```sh
"$APP" --config "$CONFIG" collection set-enabled \
  --collection-id "$COLLECTION_ID" --enabled=false
"$APP" --config "$CONFIG" collection remove \
  --collection-id "$COLLECTION_ID" --yes
```

Removing a collection removes its stored collection data and associated
derived records. Back up first if the collection may be needed later.

Scan the collection recursively:

```sh
"$APP" --config "$CONFIG" scan --collection-id "$COLLECTION_ID"
"$APP" --config "$CONFIG" scan status
```

The scan records supported, unsupported, corrupt, unstable, missing, and
duplicate-path outcomes. It prints a scan run ID and enqueues an analysis job
for the collection. A later scan reconciles changes and only sends newly
needed work through the pipeline.

Scan runs can be controlled with their ID:

```sh
"$APP" --config "$CONFIG" scan pause SCAN_RUN_ID
"$APP" --config "$CONFIG" scan resume SCAN_RUN_ID
"$APP" --config "$CONFIG" scan cancel SCAN_RUN_ID
"$APP" --config "$CONFIG" scan retry SCAN_RUN_ID
```

## 4. Start the processing worker

Run the worker in a second terminal and leave it running while jobs are
queued:

```sh
"$APP" --config "$CONFIG" worker \
  --pipeline-id "$PIPELINE_ID" \
  --script "$PWD/worker/insightface_worker.py"
```

The worker uses `python_path` from the configuration and
`worker/insightface_worker.py` by default. Both can be overridden for one
invocation with `--python PATH` and `--script PATH`.

The worker starts the Python protocol process, verifies the pipeline contract,
and processes the queue in this order:

1. analysis detects faces and stores embeddings;
2. recognition derives candidate assignments and recommendations;
3. clustering groups unresolved faces;
4. reference changes enqueue targeted recognition and clustering work.

Monitor or control jobs from another terminal:

```sh
"$APP" --config "$CONFIG" job list
"$APP" --config "$CONFIG" job status --job-id JOB_ID
"$APP" --config "$CONFIG" job pause JOB_ID
"$APP" --config "$CONFIG" job resume JOB_ID
"$APP" --config "$CONFIG" job retry JOB_ID
"$APP" --config "$CONFIG" job get JOB_ID
"$APP" --config "$CONFIG" job cancel JOB_ID
"$APP" --config "$CONFIG" job throttle --mode reduced
"$APP" --config "$CONFIG" worker status
```

The worker can be stopped with `worker stop`. Failed item details are
available through `job get JOB_ID`, `job status --job-id JOB_ID`, and the
review service. Use `job retry JOB_ID` only after checking the failure; use
`job cancel JOB_ID` when the work should not be retried. Throttle modes are
`automatic`, `full-speed`, `reduced`, and `paused`.

Reference changes can be processed explicitly when a person gains or loses
approved reference faces:

```sh
"$APP" --config "$CONFIG" job reference-reprocess --person-id PERSON_ID
"$APP" --config "$CONFIG" job reference-discovery --since-last
"$APP" --config "$CONFIG" job reference-discovery --person-id PERSON_ID \
  --since 2026-01-01T00:00:00Z
```

To look for possible matches for unresolved faces, run the separate candidate
discovery job. It compares eligible faces with approved reference faces and
never changes assignments or cluster membership:

```sh
JOB_JSON=$("$APP" --config "$CONFIG" job possible-candidates --all)
"$APP" --config "$CONFIG" job list
"$APP" --config "$CONFIG" candidate run list
"$APP" --config "$CONFIG" candidate run get --run-id RUN_ID
"$APP" --config "$CONFIG" candidate run status --run-id RUN_ID
"$APP" --config "$CONFIG" face candidates --face-id FACE_ID
```

Use `--person-id PERSON_ID` or `--collection-id COLLECTION_ID` instead of
`--all` for a narrower run. Candidate runs have their own IDs and detail/status
commands. Candidate rows are shown on photo and person review pages, where a
reviewer can inspect the scores and explicitly assign a selected candidate.
The candidate job does not automatically promote a candidate to an assignment.

## 5. Start the review service

Start the authenticated local web service in another terminal:

```sh
"$APP" --config "$CONFIG" serve
```

The service uses `serve_address` from the configuration. Override it for one
invocation with `serve --address HOST:PORT`.

On first startup, the service creates the bootstrap system administrator
`admin` with temporary password `admin`. Log in at
<http://127.0.0.1:8080/login> and change it immediately. Passwords must be at
least eight characters and contain letters and numbers.

Useful pages include:

- <http://127.0.0.1:8080/> — dashboard;
- <http://127.0.0.1:8080/review/photos> — photos and detected faces;
- <http://127.0.0.1:8080/review/people> — people records;
- <http://127.0.0.1:8080/review/clusters> — clustering results;
- <http://127.0.0.1:8080/admin/accounts> — account administration;
- <http://127.0.0.1:8080/admin/collections> — collection permissions.

Additional accounts can be created by a system administrator, either in the
web UI or with the CLI:

```sh
"$APP" --config "$CONFIG" account create \
  --username reviewer --password 'replace-with-a-strong-password' \
  --role reviewer
"$APP" --config "$CONFIG" account list
"$APP" --config "$CONFIG" account get --account-id ACCOUNT_ID
"$APP" --config "$CONFIG" account grant \
  --account-id ACCOUNT_ID --collection-id "$COLLECTION_ID" \
  --view --modify --undo
```

Administrators can disable or re-enable an account and force a password
change. Collection permissions are independent of the account role: `view`
allows review access, `modify` allows review changes, and `undo` allows
reverting changes.

```sh
"$APP" --config "$CONFIG" account disable --account-id ACCOUNT_ID
"$APP" --config "$CONFIG" account enable --account-id ACCOUNT_ID
"$APP" --config "$CONFIG" account reset-password \
  --account-id ACCOUNT_ID --password 'replace-with-a-strong-password'
```

## 6. Review people, faces, and clusters

The initial cluster run happens as part of the worker processing a scan.

Clustering can be re run by the worker, or explicitly for an investigation:

```sh
"$APP" --config "$CONFIG" cluster run \
  --collection-id "$COLLECTION_ID" --scope all
"$APP" --config "$CONFIG" cluster run list
"$APP" --config "$CONFIG" cluster run get --run-id RUN_ID
"$APP" --config "$CONFIG" cluster run status --run-id RUN_ID
"$APP" --config "$CONFIG" cluster list
"$APP" --config "$CONFIG" cluster get --cluster-id CLUSTER_ID
```

`cluster run` returns a clustering-run ID. Use the `cluster run` commands to
inspect that run; it is different from the cluster IDs returned by
`cluster list --run-id RUN_ID`.

## 7. Administration dashboard

System administrators can open the web dashboard at `/admin`. It provides
paginated, filterable, and sortable views of jobs, clustering runs, scans,
audit events, pipelines, accounts, and collections. Selecting an item opens
a detail table containing the stored fields for that object.

The account detail page supports password resets, account disablement, and
collection permission changes. Accounts, collections, and pipeline records
can be added from their respective pages. Jobs, scans, and clustering runs
are operational records and are created by their normal CLI or worker
commands; the dashboard links to their results rather than allowing invalid
rows to be entered manually.

The dashboard also includes `/admin/status` and `/admin/setup-check`, which
show the status and setup-check snapshots captured when the web service
starts.

Create a named person before confirming assignments:

```sh
PERSON_ID=$("$APP" --config "$CONFIG" people create \
  --name "Alex Example" --yes)
"$APP" --config "$CONFIG" people list
"$APP" --config "$CONFIG" face list
"$APP" --config "$CONFIG" face get --face-id FACE_ID
```

Identity-changing commands require `--yes`. Assignment state and disposition
are independent:

```sh
# Confirm or correct an assignment.
"$APP" --config "$CONFIG" face assign \
  --face-id FACE_ID --person-id "$PERSON_ID" \
  --state assigned-user --yes

# Other assignment states include unassigned, unknown, and ambiguous.
"$APP" --config "$CONFIG" face assign \
  --face-id FACE_ID --state ambiguous --yes

# Record a false detection or exclude a face from default review lists.
"$APP" --config "$CONFIG" face disposition \
  --face-id FACE_ID --disposition false-detection --yes
"$APP" --config "$CONFIG" face disposition \
  --face-id FACE_ID --disposition dont-track --yes
```

To exclude a person from candidate discovery, set the person-level
don't-track flag. This is separate from a face disposition:

```sh
"$APP" --config "$CONFIG" people dont-track \
  --person-id "$PERSON_ID" --value=true
```

Cluster review supports a default person plus explicit per-face exceptions:

```sh
"$APP" --config "$CONFIG" cluster review \
  --cluster-id CLUSTER_ID --default-person-id "$PERSON_ID" \
  --status reviewed --yes
```

The web API additionally supports rejecting an assignment and restoring its
previous state with `POST /api/faces/FACE_ID/assignment` and
`{"reject":true}`. Use the authenticated review pages or APIs when a
multi-face review needs optimistic version checking.

## 8. Select reference faces and search

Select only clear, representative faces as references. Reference selection
causes targeted recognition and clustering reprocessing, so keep the worker
running:

```sh
"$APP" --config "$CONFIG" face reference \
  --face-id FACE_ID --person-id "$PERSON_ID" --reference --yes

# Remove the selection explicitly when needed.
"$APP" --config "$CONFIG" face reference \
  --face-id FACE_ID --person-id "$PERSON_ID" --reference=false --yes
```

Each person also has a UI-only representative image. By default it is the
highest-quality eligible face from references, then user, system, and
provisional assignments. Choose a face explicitly when the automatic choice
is not suitable:

```sh
"$APP" --config "$CONFIG" people representative \
  --person-id "$PERSON_ID" --face-id FACE_ID --yes

# Clear the explicit choice and return to automatic selection.
"$APP" --config "$CONFIG" people representative \
  --person-id "$PERSON_ID" --clear --yes
```

The authenticated equivalents are `GET /api/people/PERSON_ID/representative`
and `POST /api/people/PERSON_ID/representative` with either
`{"face_id":"FACE_ID"}` or `{"clear":true}`. The generated crop is stored
in SQLite, is at most 96x96 pixels, and is not used for recognition.

Search supports person, relative-directory, capture-date, provisional, and
review-state filters:

```sh
"$APP" --config "$CONFIG" search --person-id "$PERSON_ID"
"$APP" --config "$CONFIG" search --person-id "$PERSON_ID" \
  --directory vacations --from 2020-01-01 --until 2024-12-31
"$APP" --config "$CONFIG" search --review-mode
"$APP" --config "$CONFIG" search --review-mode \
  --include-provisional --include-assigned-system --include-assigned-user
"$APP" --config "$CONFIG" search --states unassigned,ambiguous
```

In review mode, assigned faces are excluded by default. Add the relevant
`--include-*` flags to include provisional, system-assigned, or user-assigned
faces. `--states` accepts a comma-separated assignment-state filter. Use
`--json` for versioned machine-readable output from CLI commands, and the
authenticated `/api/photos` endpoints for browser or application
integrations.

Generate score-distribution reports for active clusters, either globally or
for selected collections:

```sh
"$APP" --config "$CONFIG" chart --all --format markdown
"$APP" --config "$CONFIG" chart --collection-id "$COLLECTION_ID" \
  --format json
```

## 9. Incremental processing and maintenance

After adding or removing files, run the same scan again. Inspect the new scan
and queued jobs before reviewing new clusters:

```sh
"$APP" --config "$CONFIG" scan --collection-id "$COLLECTION_ID"
"$APP" --config "$CONFIG" scan status
"$APP" --config "$CONFIG" job list
```

Create a database backup before large review or configuration changes. Stop
the worker and service before restoring one. Stop the worker with `worker
stop`; stop the review service with `Ctrl-C` in its terminal or through the
process supervisor that launched it:

```sh
BACKUP=$("$APP" --config "$CONFIG" backup create)
"$APP" --config "$CONFIG" restore --input "$BACKUP" --yes
"$APP" --config "$CONFIG" status
"$APP" --config "$CONFIG" setup check
```

After restoring, verify the configured paths and setup report before starting
the worker and review service again. A restore replaces the configured
database; it does not restore model files or photos.

Exports are JSON and can contain sensitive data, especially embeddings:

```sh
"$APP" --config "$CONFIG" export metadata --output exports/metadata.json
"$APP" --config "$CONFIG" export assignments --output exports/assignments.json
"$APP" --config "$CONFIG" export embeddings --output exports/embeddings.json
```

Audit history can be inspected or retained data can be purged according to
the deployment policy:

```sh
"$APP" --config "$CONFIG" audit list
"$APP" --config "$CONFIG" audit purge --age-days 365
```

To rebuild all derived face, assignment, clustering, and job data while
retaining source collection and account records, stop the worker and service,
create a backup, and explicitly confirm:

```sh
"$APP" --config "$CONFIG" purge derived --yes
```

This operation is destructive and cannot recover the deleted derived data by
itself. Rescan and reprocess collections afterward.

For a complete command list, run:

```sh
"$APP" help
"$APP" help worker
"$APP" help search
"$APP" help purge
```
