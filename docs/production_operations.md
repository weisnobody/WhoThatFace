# Production operations

The MVP runs as a Go CLI/service backed by SQLite and a persistent Python
InsightFace worker. The default runtime uses system Python 3.11 with a local
`.venv`; Docker is optional.

## Initial setup

```sh
./scripts/setup_worker.sh
source .venv/bin/activate
go build -o bin/face-me-bro ./cmd/face-me-bro
cp config.example.jsonc config.json
```

Edit `config.json` so the database, runtime data root, worker command, model
root, provider, and recognition/clustering settings point at the intended
machine. Run `./bin/face-me-bro setup check` before processing photos.

## Normal processing

Initialize the database and create a collection, then scan it:

```sh
./bin/face-me-bro collection add --name family --root /photos/family
./bin/face-me-bro scan --collection-id COLLECTION_ID
./bin/face-me-bro worker --pipeline-id PIPELINE_ID
```

The scan records assets and paths. The analysis job sends each distinct asset
to the worker with its persisted SHA-256 and size, stores faces and embeddings,
then queues recognition and clustering. Only the worker process loads
InsightFace; model/provider settings come from the selected pipeline and the
database-backed application configuration.

Monitor work with:

```sh
./bin/face-me-bro job list
./bin/face-me-bro job status --job-id JOB_ID
./bin/face-me-bro worker status
```

Start the authenticated review service with `./bin/face-me-bro serve` and
visit `http://127.0.0.1:8080/review/people`, `/review/photos`, or
`/review/clusters`. Reviewers can assign faces, mark unknown/ambiguous or
don't-track, approve references, and inspect recommendation evidence.

## Recovery and diagnostics

Use `job retry --job-id JOB_ID` for failed items after correcting the cause.
The worker client restarts the worker after malformed output, timeout, crash,
or cancellation. `worker stop` terminates a foreground worker recorded in the
runtime PID file. Before destructive maintenance, use `backup create`, export
metadata when needed, and use the audited `purge` command only with its
confirmation flag.

The CLI and JSON APIs expose operational state. The web review pages are the
primary UI; detailed bulk export, pagination, migration, backup, and worker
lifecycle operations remain CLI-oriented in the MVP.
