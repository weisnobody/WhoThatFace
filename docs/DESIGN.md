# System Design

This document describes the implementation structure and architectural
boundaries of `face_me_bro`. Required behavior and acceptance criteria are
defined in [SPEC.md](SPEC.md). Larger implementation milestones belong in
[PLAN.md](PLAN.md). Smaller task, feature, and bug-fix plans belong in
the `docs/plans/` directory.

## Repository layout

```text
face_me_bro/
├── AGENTS.md
├── README.md
├── cmd/
│   ├── face-me-bro/
│   └── face-me-bro-benchmark/
├── internal/
│   ├── analysis/
│   ├── clustering/
│   ├── jobs/
│   ├── media/
│   ├── recognition/
│   ├── review/
│   └── web/
├── docs/
│   ├── DESIGN.md
│   ├── SPEC.md
│   ├── PLAN.md
│   └── plans/
│       └── YYYY-MM-DD-description.md
├── schemas/
│   ├── schema/
│   └── migrations/
├── worker/
│   ├── insightface_worker.py
│   ├── requirements.in
│   ├── requirements.lock
│   └── platforms.json
├── scripts/
│   ├── setup_worker.sh
│   └── setup_models.py
├── docker/
│   └── Dockerfile
└── ...
```

### Top-level directory responsibilities

- `cmd/` contains Go entry points for the application and benchmark command.
- `internal/` contains the Go application services and package boundaries.
- `worker/` contains the Python ML adapter, dependency locks, and provider
  metadata.
- `scripts/` contains environment and model setup automation.
- `docker/` contains the optional worker-container configuration.
- `schemas/schema/` contains the canonical database schema definitions or
  schema artifacts.
- `schemas/migrations/` contains ordered, immutable database migrations.
- `docs/` contains requirements, design, plans, and task-specific planning
  documents.
- `docs/plans/` contains plans for smaller tasks, features, and bug fixes.
- `README.md` contains the project overview, setup entry point, and links to
  detailed documentation.

Generated artifacts, model files, virtual environments, databases, logs,
backups, exports, private evaluation data, and embeddings must not be stored
in the source tree unless explicitly intended as a small test fixture.

## Architecture overview

The system has four primary runtime elements:

```text
                 ┌────────────────────┐
                 │       CLI          │
                 └─────────┬──────────┘
                           │
                 ┌─────────▼──────────┐
                 │   Go application   │
                 │ services/jobs      │
                 └───┬─────────────┬──┘
                     │             │
             ┌───────▼──────┐ ┌────▼────────────┐
             │    SQLite    │ │ Python worker   │
             │ system state │ │ InsightFace ML │
             └──────────────┘ └────┬────────────┘
                                   │
                             ┌─────▼─────┐
                             │ model files│
                             └───────────┘
```

Go owns application state and orchestration. Python is a replaceable ML
adapter. The Python worker does not access SQLite or own durable application
state.

## Component boundaries

### Go application

The Go application owns:

- CLI commands and output formatting.
- Collection configuration and filesystem scanning.
- File stability checks and SHA-256 identity.
- EXIF and photo metadata persistence.
- SQLite transactions and migrations.
- Persistent jobs and job recovery.
- People and identity-event management.
- Embedding persistence and compatible-pipeline checks.
- Similarity matching and recognition decisions.
- Face-to-face similarity search and clustering, with the initial grouping
  implementation preferred in Go.
- Search.
- Worker lifecycle, protocol handling, timeout, restart, and retry logic.
- Adaptive resource control.

The Go application should expose application services that do not depend on
CLI formatting. CLI commands translate user input into service calls and
render human-readable or structured output.

### Python worker

The Python worker owns:

- InsightFace model loading.
- Supported image decoding for inference.
- EXIF orientation for the inference pipeline.
- Face detection and alignment.
- Embedding generation.
- Face-quality metrics.

The worker communicates through the versioned NDJSON protocol described in
`SPEC.md`. stdout is reserved for protocol messages; diagnostics use stderr.

### SQLite

SQLite is the durable system of record for application state, including:

- collections
- filesystem paths
- deduplicated photo assets
- analyses and pipeline provenance
- faces and embeddings
- people and identity history
- scans and other jobs
- clustering runs
- schema migrations

The database contains logical collections rather than one database per photo
collection. This enables shared people, cross-collection search, and
cross-collection content deduplication.

## Core data flow

### Ingestion and analysis

```text
collection root
  → recursive scanner
  → file stability check
  → SHA-256 content identity
  → photo asset and path records
  → metadata extraction
  → analysis job
  → Python worker
  → versioned analysis
  → face records and binary embeddings
```

The asset/path distinction is fundamental:

```text
photo asset/content
  └── one or more filesystem paths
          └── each path belongs to a collection
```

The asset owns deduplicated content and embedded metadata. Each path owns its
collection membership, directory, current/missing state, and filesystem
observation data.

## User-facing processing workflow

The normal workflow is discovery-first and incremental. A user may add photos
without having reference faces for every person in the collection.

### 1. Add photos

The user adds one or more directories or paths to the system. The scanner
creates or updates path and asset records, deduplicates content by hash, and
queues new or changed photos for analysis.

### 2. Process new photos

For each new photo, the system:

1. Detects faces and records bounding boxes, detection confidence, quality
   metrics, and embeddings.
2. If reference faces exist, compares eligible faces with the reference gallery.
3. Records recognition results for tracked people as `assigned-system`,
   `provisional`, or `unknown` according to the assignment policy.
4. Groups all eligible newly detected faces using the configured clustering
   workflow. A later configuration may restrict grouping to faces without a
   high-confidence recognition result.
5. Generates reference-face recommendations for faces with a person
   assignment, including system-assigned and user-assigned faces.

Recognition and grouping are separate evidence paths. Recognition compares a
face with person reference examples. Grouping compares faces with other faces.
Both results are retained so the user can understand disagreement between the
two processes.

### 2.1 Cluster disposition

When cluster consistency, face quality, detection confidence, and recognition
evidence agree that the faces likely belong to one person, the system may take
one of these dispositions:

- If the recognized person is in the `don't track` pool, retain the person
  assignment and the independent don't-track state; don't-track does not alter
  recognition or grouping eligibility.
- If the cluster joins an existing cluster that has already been manually
  assigned to a person, provisionally assign new faces to that person.
- Otherwise, provisionally associate the cluster's faces with the suggested
  person so they can appear in that person's photo collection while the cluster
  remains marked for review.

If the evidence disagrees, the cluster is flagged as requiring review. A mixed
cluster must remain explicitly reviewable and must not be bulk-confirmed as one
person.

Provisional association is a review state, not a final identity confirmation.
It must be visibly distinguishable from a user-confirmed assignment and must
not automatically promote every face to the reference gallery.

### 2.2 Reference recommendations

After processing and assignment, the system may recommend faces with a person
assignment as reference candidates, including system-assigned or
user-assigned faces. A don't-track disposition does not exclude a face that
has a person assignment. Recommendations consider face quality, detection
confidence, similarity to the person's embedding center, cluster consistency,
diversity, and duplicate risk. Recommendations are advisory; the user approves
which faces become references.

### 3. Review clusters

The user can open a cluster-review workspace containing clusters requiring
attention. The cluster list supports:

- Sorting by face count, candidate count, or associated person.
- Filtering by person, face-count range, mixed-cluster status, and review
  rating.
- Filtering by `don't track`, which is excluded by default.
- Selecting a cluster for detailed review.

Review rating is derived from configurable ranges of signal, quality,
detection, and recognition-confidence values. Ratings are prioritization aids,
not identity decisions.

Singleton clusters are displayed separately or grouped into a virtual review
group by primary person. Assigning a virtual singleton group requires an
additional confirmation that the user reviewed the individual faces and is not
assuming that the group is a true visual cluster.

Within a cluster, the user can:

- Sort faces by confidence, quality, or detection confidence.
- Filter faces by person or reference recommendation status.
- Set or change the proposed person for all faces in the cluster.
- Reject the proposed person for individual faces.
- Assign individual faces through a person selector with search/autocomplete.
- Create a new person and assign selected faces to it.
- Confirm the selected faces for the proposed person.
- Assign selected faces to the `don't track` pool.

Confirmation controls should be available at both the beginning and end of a
cluster view when the view is long enough to require scrolling.

### 4. Review people

The people workspace provides a person-centered view of review work. It
supports sorting by:

- Name.
- Number of photos.
- Number of clusters containing more than one face.
- Number of singleton clusters.
- Number of reviewed clusters.

It supports filtering by:

- Name.
- Whether the person has enough approved reference faces.
- Whether the person has clusters requiring review.
- Favorites, when that future feature exists.
- Groups, when that future feature exists.
- Unknown people who have no name and are not in `don't track`.
- `Don't track`, excluded by default.

Person actions include:

- Open clusters requiring review for that person.
- View approved references and recommended reference candidates.
- View low-confidence or conflicting assignments.

The low-confidence view includes faces that are currently unassigned to the
person but for which one evidence path indicates that the person remains a
candidate. The default action is to confirm that the face is not that person;
the user can instead assign it to the person.

### 5. Review questionable assignments (future feature)

The system may later provide a queue for assignments that conflict strongly
with recognition evidence. The user can confirm the assignment, assign another
person, or place the face in `don't track`.

### 6. Tag non-face people (future feature)

The system may later allow users to identify a person who is present but has no
usable face, such as a person facing away from the camera. The initial version
may identify bodies and allow a user to mark a body or other region, but these
marks are search metadata rather than face-recognition training examples.

The data model and review experience for body-only identification require a
separate design decision.

### 7. Reprocess after reference changes

When the user approves new reference faces, the system identifies affected
people and queues only the work that can be improved by the changed reference
set. Existing embeddings remain unchanged. Stored predictions and provisional
assignments are versioned or marked stale so the user can distinguish results
produced before and after the reference change.

The initial priority order is:

| Priority | Work |
| --- | --- |
| 1 | Recognition predictions |
| 2 | Cluster dispositions |
| 3 | Provisional person assignments |
| 4 | Reference recommendations |
| 9 | Search indexes |

The queue may coalesce duplicate work for the same person, face, or collection.
Search-index updates are intentionally lowest priority because they do not
change recognition or review correctness.

### Recognition

```text
query face
  → compatible approved reference faces
  → cosine similarity
  → person-level evidence aggregation
  → threshold, margin, support-count, and quality checks
  → suggestion or unknown
  → user confirmation/rejection
  → identity event
```

Identity events are retained as history. The current confirmed identity is a
derived state, not the only stored record.

## Production grouping

The MVP runs face grouping over all eligible faces in the collection, including
faces that already have recognition results. This preserves a second evidence
path and makes mixed-cluster disagreements visible during review. The candidate
scope is configurable so a later deployment can group only faces without a
high-confidence recognition result; the default remains `all`.

Recognition and grouping use separate thresholds. The initial recognition
defaults are:

| Recognition context | Similarity | Margin | Grouping `k` | Face-to-face edge threshold |
| --- | ---: | ---: | ---: | ---: |
| Person has approved references | 0.25 | 0.05 | 40 | 0.45 |
| Person has no approved references | 0.40 | 0.05 | 40 | 0.45 |

All values are configurable. Similarity and margin apply to person recognition;
the face-to-face threshold applies when constructing grouping graph edges.
Grouping uses a k-nearest-neighbor graph with mutual-nearest-neighbor edges in
the initial implementation. Connected components form the provisional groups.

## Assignment and membership states

The system records the evidence and user disposition separately, while
exposing the following operational states. Stored assignment states are shown
in parentheses where the display label differs:

- **Recognized/high-confidence (`assigned-system`):** clustering and recognition evidence are high
  quality and agree on the person.
- **Provisional person association (`provisional`):** clustering and recognition evidence are at
  least medium quality and agree on the person, but the user has not confirmed
  the assignment.
- **User-confirmed assignment (`assigned-user`):** the user explicitly assigned the face to the
  person.
- **Unassigned or unknown (`unassigned`/`unknown`):** no effective person
  assignment exists.
- **Ambiguous (`ambiguous`):** the user reviewed the face but could not identify
  the person.
- **Incorrect assignment (`incorrect-assignment`):** the detected face is real,
  but the system's proposed person is wrong.
- **False detection (`false-detection`):** the detected region is not a usable
  face.
- **Mixed-cluster membership (derived):** the face belongs to a cluster containing faces
  assigned to different people.
- **Don't-track membership (independent disposition):** the user indicated that the person is not
  important to track. A face may have a person assignment and also be marked
  don't-track, or may be marked don't-track without a person assignment.

These states must not be collapsed into a single nullable person field. In
particular, a provisional association is not equivalent to user confirmation,
and `don't track` exists both as a person state and a face-level disposition.

## Clustering run data

Each clustering execution is an immutable, inspectable run. It records:

- algorithm name and implementation/version;
- `k` and face-to-face similarity threshold;
- neighbor-search method and its configuration;
- the input face-set definition and membership snapshot;
- cluster memberships and component identifiers;
- review status and mixed-cluster state;
- run date/time;
- superseded or replaced run identifiers.

New runs do not overwrite prior results. A current-run relationship allows the
application to use the latest accepted run while retaining reproducibility and
review history.

Low-signal thresholds for quality, detection, signal, and related metrics are
used for report filters and review-priority flags. They do not silently change
cluster membership or identity decisions.

## Similarity-search scale and rebuild strategy

The exact-versus-ANN boundary is a configuration choice that may be set
globally or per collection. The following planning table is a starting point,
not a hard product limit:

| Face count | Initial search strategy | Reason |
| ---: | --- | --- |
| < 10,000 | Exact, batched search | Simplest and easy to validate |
| 10,000–50,000 | Exact, batched search | Still practical on target hardware |
| 50,000–100,000 | Benchmark exact versus ANN | Transition depends on latency and memory |
| 100,000–250,000 | ANN recommended; exact validation samples | Better interactive and incremental performance |
| > 250,000 | ANN default with periodic exact validation | Exact all-pairs work becomes increasingly costly |

Supported rebuild/update strategies have different tradeoffs:

| Strategy | Advantages | Costs/risks |
| --- | --- | --- |
| Full rebuild | Simple, deterministic, easiest to validate | Recomputes all neighbors and clusters |
| Incremental update | Fast for small additions | Can become stale or inconsistent at component boundaries |
| Append plus periodic rebuild | Good operational balance | Results can change at rebuild time |
| Affected-subgraph rebuild | Limits work to impacted regions | More complex invalidation and correctness rules |
| ANN with periodic exact validation | Scales to large libraries | Requires index lifecycle, tuning, and validation |

## Review-service architecture

The review interface is a web service placed behind a reverse proxy that
terminates TLS and provides certificates. Authentication and authorization are
application concerns even when the proxy handles transport security.

The service must support:

- accounts and authenticated sessions;
- granting accounts access to specific collections;
- recording which account confirmed an assignment or selected a reference;
- audit history for review actions;
- an explicit permission to undo another user's action.

Review writes go through application services and transactions rather than
direct browser access to SQLite. The CLI and future API clients should use the
same authorization and audit rules.

## Process and job model

Long-running work is represented as persisted jobs rather than only as active
terminal processes. Jobs must expose state, progress, errors, and retry
information.

Initial job categories include:

- collection scan
- image analysis
- reprocessing for a new ML pipeline
- recognition suggestion generation
- clustering
- maintenance and index rebuilds

Jobs support, where meaningful:

- pending
- running
- paused
- cancelling
- cancelled
- completed
- completed with errors
- failed

The application must be able to resume work after process interruption. A
worker failure affects the current item/job attempt, not the integrity of the
database or unrelated jobs.

## Worker lifecycle

The Go application starts a persistent Python worker when analysis is needed.
The worker loads its model once and processes one request at a time in the
MVP.

```text
start worker
  → negotiate/check protocol and pipeline
  → submit analyze request
  → receive response or timeout
  → persist result or categorized failure
  → restart worker on fatal failure
```

Cancellation is implemented by terminating and restarting the worker in the
MVP. A future protocol may support cooperative cancellation if needed.

## Runtime data layout

The default macOS runtime data root is
`~/Library/Application Support/face_me_bro/`. The database is
`face_me_bro.sqlite3` under that root. Configuration, models, logs, temporary
files, exports, and backups use separate subdirectories.

The CLI supports an alternate configuration file or configuration location.
That configuration may set an alternate runtime data root, including a root on
an external drive. Status and diagnostic commands report the resolved
configuration file, runtime data root, database path, model path, and active
pipeline.

Platform-specific default paths are provided through a path-resolution
adapter; configured paths and application behavior remain platform independent.
The design separates runtime data into these categories:

- SQLite database.
- Configuration.
- Python virtual environment.
- Downloaded and checksum-verified model files.
- Logs and diagnostics.
- Backups and exports.
- Temporary processing files.

Runtime data must not be placed in the repository by default. Model files,
databases, backups, exports, and embeddings are sensitive local data.

The default runtime uses system Python plus a project-managed virtual
environment. Docker is an optional reproducible environment, not the default
macOS runtime. Model and dependency downloads occur during explicit setup;
normal processing is offline.

## Background resource policy

The application provides three processing modes:

- **Automatic:** use available resources when the machine appears idle and
  reduce concurrency/intensity during user activity.
- **Full speed:** use maximum configured processing resources.
- **Reduced or paused:** user-controlled limitation or pause.

Automatic mode uses heuristics such as recent activity, system load, thermal
state, power state, and application activity. These signals are advisory. A
manual override is required when the system cannot reliably infer user
activity.

Throttling changes throughput only. It must not alter analysis or recognition
results.

## Extension points

### ML implementation

The Go-side recognizer abstraction isolates application behavior from the
InsightFace implementation. The initial path is:

```text
Go Recognizer interface
  → Python NDJSON worker
      → InsightFace
```

A future implementation may be:

```text
Go Recognizer interface
  → direct ONNX/CoreML implementation
```

Pipeline IDs ensure that embeddings from incompatible implementations are not
silently compared.

### Similarity search

The initial matcher may scan all eligible embeddings. A vector index or
person-level representative vectors can be added later behind the matching
service, with exact matching retained as a correctness/reference path.

### Presentation layers

The CLI and authenticated web review service are the MVP presentation layers.
Future native GUI or additional API layers should call the same application
services and job model rather than access SQLite or the Python worker directly.

## Key design tradeoffs

### One database versus one database per collection

One database with logical collections was selected because it supports shared
people, cross-collection search, global deduplication, and simpler migrations.
Collection-specific backup and deletion require explicit tooling.

### Content assets versus path records

Content assets allow duplicate files to be analyzed once. Path records retain
every location and support directory search, moves, missing paths, and
collection membership.

### Binary embeddings versus JSON

Embeddings are stored as complete binary float32 vectors for compactness and
efficient processing. JSON remains an export/debug representation.

### Historical identity events versus mutable assignments

Historical events preserve suggestions, confirmations, rejections, manual
assignments, revocations, scores, and provenance. A derived current state
keeps normal queries simple without losing audit history.

### Confirmation versus automatic assignment

Recognition produces suggestions and may record provisional or
system-assigned states. User confirmation is required before an identity is
treated as user-confirmed in the MVP. Faces with a person assignment are
eligible for reference recommendations, but only faces explicitly approved and
marked as references become future matching examples. This prevents false
positives or poor-quality faces from silently entering the recognition gallery.

### Python worker versus direct model execution

The Python worker minimizes initial ML integration effort and isolates model
dependencies. The recognizer boundary preserves a future path to direct
ONNX/CoreML execution if packaging or performance requires it.

## Security and repository boundaries

- Original images are never modified.
- The runtime does not require cloud access.
- No telemetry is collected.
- Embeddings are treated as sensitive biometric-derived data.
- Logs should avoid full paths and sensitive metadata unless diagnostic mode is
  explicitly enabled.
- Private evaluation images, labels, embeddings, and identifying metadata must
  not be committed to source control.
- Model and dependency licenses must be reviewed before distribution.

## Documentation boundaries

| Document | Responsibility |
| --- | --- |
| `README.md` | Project overview, quick start, and documentation links |
| `docs/SPEC.md` | Requirements, contracts, data model, constraints, and acceptance criteria |
| `docs/DESIGN.md` | Architecture, implementation boundaries, repository layout, and tradeoffs |
| `docs/PLAN.md` | Larger implementation milestones |
| `docs/plans/` | Plans for smaller tasks, features, and bug fixes |
| `docs/installation.md` | Installation, model setup, and worker startup |
| `worker/README.md` | Worker-specific setup and development details |

When these documents conflict, the approved specification defines required
behavior; this document explains implementation organization; plans describe
sequencing rather than requirements.
