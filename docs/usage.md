# Example usage: build a searchable family photo library

This walkthrough shows a typical discovery-first workflow: add a collection,
let the system detect and group faces, review the uncertain results, create and
name people, select approved reference faces, and search the resulting library.
It assumes the Go binaries and Python worker have already been built as
described in [the README](../README.md) and [installation guide](installation.md).

## 1. Initialize the local system

From the repository root, install the worker environment and build the Go
executables:

```sh
./scripts/setup_worker.sh
go mod download
mkdir -p bin
go build -o bin/face-me-bro ./cmd/face-me-bro
go build -o bin/face-me-bro-benchmark ./cmd/face-me-bro-benchmark
```

Set the model environment used by the worker. Adjust the root if a custom
`FACE_ME_BRO_MODEL_ROOT` was used during setup:

```sh
export FACE_ME_BRO_MODEL_ROOT="$PWD/runtime/models"
export FACE_ME_BRO_MODEL=buffalo_l
```

Open the database and apply migrations by checking the status:

```sh
./bin/face-me-bro status
./bin/face-me-bro help
```

The first time the web service starts it creates the system administrator
account `admin` with temporary password `admin`. Change it immediately after
logging in.

## 2. Register the processing pipeline

Register the exact model contract used by the worker. The checksum manifest
created by setup is passed to the pipeline record so worker startup can verify
it:

```sh
CHECKSUMS="$PWD/runtime/models/models/buffalo_l/checksums.json"
PIPELINE_ID=$(./bin/face-me-bro pipeline register \
  --model-family buffalo_l \
  --model-version 2.0.0 \
  --provider CPUExecutionProvider \
  --model-checksums-json "$(<"$CHECKSUMS")")

./bin/face-me-bro pipeline list
```

If the model root or setup location is different, use the corresponding
`checksums.json` path. The registered pipeline records the model, provider,
embedding format, detector settings, preprocessing, and checksums.

## 3. Add and scan photo collections

Add one directory as a collection. The command prints the collection UUID;
save it for subsequent commands:

```sh
PHOTO_ROOT="/path/to/Family Photos"
COLLECTION_ID=$(./bin/face-me-bro collection add \
  --name "Family Photos" --root "$PHOTO_ROOT")
./bin/face-me-bro collection list
```

Choose the grouping search method. `exact` is the correctness/reference path;
`ann` is the evaluated approximate-search prototype:

```sh
./bin/face-me-bro collection set-search \
  --collection-id "$COLLECTION_ID" --method exact
```

Scan the directory recursively. Each distinct file content is analyzed once,
while every path remains represented in the database:

```sh
./bin/face-me-bro scan --collection-id "$COLLECTION_ID"
./bin/face-me-bro scan status
```

The scan output includes an analysis job ID. Unsupported, corrupt, missing,
and duplicate-path outcomes can be inspected through the scan status and web
review pages.

## 4. Start processing

Run the worker in a second terminal and leave it running while the collection
is processed:

```sh
cd /path/to/face_me_bro
export FACE_ME_BRO_MODEL_ROOT="$PWD/runtime/models"
./bin/face-me-bro worker \
  --pipeline-id "$PIPELINE_ID" \
  --python "$PWD/.venv/bin/python" \
  --script "$PWD/worker/insightface_worker.py"
```

The worker processes analysis, recognition, and clustering jobs. Monitor the
queue from another terminal:

```sh
./bin/face-me-bro job list
./bin/face-me-bro job get JOB_ID
```

When references do not yet exist, faces are kept as unknown, provisional, or
clustered discoveries. This is expected for a new library.

## 5. Open the review service

Start the authenticated local web service:

```sh
./bin/face-me-bro serve --address 127.0.0.1:8080
```

Visit these URLs in a browser:

- <http://127.0.0.1:8080/login> — sign in and change the temporary password.
- <http://127.0.0.1:8080/> — review dashboard.
- <http://127.0.0.1:8080/review/photos> — browse photos and detected faces.
- <http://127.0.0.1:8080/review/people> — list people currently known to the system.
- <http://127.0.0.1:8080/review/clusters> — list provisional face clusters.
- <http://127.0.0.1:8080/admin/accounts> — manage accounts as a system admin.
- <http://127.0.0.1:8080/admin/collections> — manage collection access.

The cluster page shows each face, its bounding box, candidate evidence, review
state, purity, connectivity, and whether the clustering run is historical. A
green border indicates a user-confirmed assignment, yellow indicates a system
or provisional assignment, and red indicates that attention is needed.

The current service exposes authenticated mutation APIs for the review UI. The
CLI examples below are convenient for scripted or single-face corrections;
browser/API clients must preserve the session and CSRF protections.

## 6. Create and name people

Use a person record for anyone who should be searchable. The `--yes` flag is
required because creating an identity changes the database:

```sh
PERSON_ID=$(./bin/face-me-bro people create \
  --name "Alex Example" --yes)
./bin/face-me-bro people list
```

The clustering process may also create a provisional, unnamed person. Review
that record and create a named person when you can identify the faces. The
person ID can be copied from `people list` or the JSON output:

```sh
./bin/face-me-bro --json people list
```

## 7. Accept, correct, reject, or suppress face assignments

Get the current state of a face before changing it:

```sh
./bin/face-me-bro face get --face-id FACE_ID
```

Use these actions while reviewing a cluster or photo:

| Review decision | Command |
| --- | --- |
| Confirm the face belongs to a person | `face assign --face-id FACE_ID --person-id PERSON_ID --state assigned-user --yes` |
| Reassign it to another person | Run the same command with the other `PERSON_ID`. |
| Mark it unknown | `face assign --face-id FACE_ID --state unknown --yes` |
| Mark it ambiguous | `face assign --face-id FACE_ID --state ambiguous --yes` |
| Reject the current system assignment and restore the previous state | Use the authenticated reject API described below. |
| Explicitly mark a face unassigned | `face assign --face-id FACE_ID --state unassigned --yes` |
| Mark it as a false detection | `face disposition --face-id FACE_ID --disposition false-detection --yes` |
| Exclude it from default review lists as don't-track | `face disposition --face-id FACE_ID --disposition dont-track --yes` |

An assignment state and a don't-track disposition are independent. A face may
be assigned to a named person and still be marked don't-track; it can still
participate in recognition and grouping, but is hidden from default review
lists.

For cluster-wide review, open a cluster at
`/review/clusters/CLUSTER_ID`. Set a default person for faces without an
existing individual decision, make explicit per-face decisions for exceptions,
choose a status such as `reviewed` or `needs-review`, and save with the current
review version. Existing explicit assignments are not overwritten by a new
default person.

The equivalent authenticated API endpoint is:

```text
POST /api/clusters/CLUSTER_ID/review
```

Its JSON body contains `default_person_id`, `status`, `expected_version`,
`note`, and a `decisions` array. Each decision can contain `face_id`,
`person_id`, `state`, and `disposition` such as `dont-track` or
`false-detection`.

To reject a face assignment and restore its previous state, use:

```text
POST /api/faces/FACE_ID/assignment
{"reject":true}
```

## 8. Select approved reference faces

Only faces explicitly selected as references are used for matching a person.
Choose clear, sharp, reasonably frontal examples with good lighting and no
occlusion. Avoid near-duplicates and faces containing another person in the
crop.

Select a face as a reference:

```sh
./bin/face-me-bro face reference \
  --face-id FACE_ID --person-id PERSON_ID --reference --yes
```

Remove the reference designation by omitting the boolean value or using the
command's false form supported by the CLI help. The selection records who made
the change and creates a reference-reprocessing job. Keep the worker running;
it will rerun recognition, affected clustering, and recommendations while
preserving user-confirmed assignments.

The authenticated API equivalent is:

```text
POST /api/faces/FACE_ID/reference
```

with a body such as `{"person_id":"PERSON_ID","reference":true}`.

## 9. Review a person's photos

Use the CLI to search a person across all accessible collections:

```sh
./bin/face-me-bro search --person-id "$PERSON_ID"
./bin/face-me-bro search --person-id "$PERSON_ID" \
  --directory "vacations" --from 2020-01-01 --until 2024-12-31
```

The authenticated JSON search endpoint supports the same person, directory,
and capture-date filters:

```text
GET /api/photos?person_id=PERSON_ID
GET /api/photos?person_id=PERSON_ID&directory=vacations&only_confirmed=true
```

Each result includes a path ID that can be opened at
`/api/photos/PATH_ID` to view the original image through the authenticated
service. The `/review/photos` page provides the browser-oriented photo list;
use the API filters when a person-specific or confirmed-only view is needed.

## 10. Add more photos later

Repeat the scan command after adding files to an existing collection:

```sh
./bin/face-me-bro scan --collection-id "$COLLECTION_ID"
```

Content hashes prevent duplicate analysis. New faces are compared with
approved references and grouped with existing discoveries. Review new clusters,
confirm assignments, and select additional high-quality reference faces as the
library becomes more familiar.

## 11. Back up and recover

Create a database backup before large review or configuration changes:

```sh
BACKUP=$(./bin/face-me-bro backup create)
```

Restore only after stopping the web service and worker and verifying the input
file. Restoration requires explicit confirmation:

```sh
./bin/face-me-bro restore --input "$BACKUP" --yes
```

Metadata can be exported separately:

```sh
./bin/face-me-bro export metadata --output exports/metadata.json
```

All identity-changing operations are recorded in the audit history. System
administrators can use the web administration pages and the audit tools to
review activity; undo operations are subject to current-state conflict checks.
