# Production operations

The MVP runs as a Go CLI/service backed by SQLite and a persistent Python
InsightFace worker. The default runtime uses system Python 3.11 with a local
`.venv`; Docker is optional.

## Initial setup

```sh
./scripts/setup_worker.sh
go build -o bin/face-me-bro ./cmd/face-me-bro
cp config.example.jsonc config.json
```

Edit `config.json` so the database, runtime data root, worker command, model
root, provider, and recognition/clustering settings point at the intended
machine. Run `./bin/face-me-bro setup check` before processing photos.

### Specifying Alternate Config File

1. Using CLI Option: `--config <file location>`
2. Environment Variable: `export FACE_ME_PRO_CONFIG="./config.jsonc"`

## Create a Pipeline

```sh
./bin/face-me-bro  pipeline register \
  --model-family buffalo_l \
  --model-version 2.0.0 \
  --model-checksums-json "./runtime/models/models/buffalo_l/checksums.json"
```

## Normal processing

Initialize the database and create a collection, then scan it:

```sh
./bin/face-me-bro collection add --name family --root DIRECTORY
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

## Human Review  

### Links

1. People: `http://127.0.0.1:8080/review/people`
2. Clusters: `http://127.0.0.1:8080/review/clusters`
3. Photos: `http://127.0.0.1:8080/review/photos`

### Steps 

This is how I've typically approached it.

1. Look at the list of people and sort by the number of Faces in descending order
  * If this is not the first time you've worked on reviewing, unchecking "User" and "System" from the "Assignment" check boxes and changing sort to "Provisional" in descending order will remove people that don't have faces that need review
2. Pick a person and see if the faces provisionally assigned are all the same person.  
  1. If all faces are the same person, Bulk assign them
  	* If not, I'll sometimes skip this person till other people are identified so the incorrect faces can be assigned ot the correct one
  1. Assign a name to the person (Rename them)
  3. If you want, set a "Key Photo" for the person
  3. Click the "Recommend References" button, wait a minute or so, then sort the faces by "Ref Recs"
  4. Select reference photos for people to faciliate the system assigning faces to people.
  	* I don't always use the ones they recommend and will use some that they haven't recommended
2. Reviewing clusters let you see how it's clustered faces.
	* The system will create a new person for the cluster if it doesn't link it with an existing person.
3. The Photos view lets you adjusts all faces in a specific photo.
	* I've used it when the system has tried to assign the same person to multiple faces as a way to more easily see / correct the problem faces.


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
