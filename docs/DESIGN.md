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
├── docs/
│   ├── DESIGN.md
│   ├── SPEC.md
│   ├── PLAN.md
│   └── plans/
│       └── YYYY-MM-DD-description.md
├── schemas/
│   ├── schema/
│   └── migrations/
├── cli/
│   ├── README.md
│   ├── cmd/
│   └── internal/
├── worker/
│   ├── README.md
│   ├── requirements.txt
│   ├── src/
│   ├── tests/
│   └── docs/
└── ...
```

### Top-level directory responsibilities

- `cli/` contains the Go application and command-line interface.
- `worker/` contains the Python ML adapter and its tests.
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
- Experimental clustering support.

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

### Recognition

```text
query face
  → compatible confirmed examples
  → cosine similarity
  → person-level evidence aggregation
  → threshold, margin, support-count, and quality checks
  → suggestion or unknown
  → user confirmation/rejection
  → identity event
```

Identity events are retained as history. The current confirmed identity is a
derived state, not the only stored record.

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

The exact platform paths are an implementation detail to finalize during the
packaging prototype. The design separates runtime data into these categories:

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

The CLI is the first presentation layer. Future GUI or API layers should call
the same application services and job model rather than access SQLite or the
Python worker directly.

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

Recognition produces suggestions. User confirmation is required for identity
assignment in the MVP. This prevents false positives from silently becoming
future training/matching examples.

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
| `cli/README.md` | CLI-specific development and usage details |
| `worker/README.md` | Worker-specific setup and development details |

When these documents conflict, the approved specification defines required
behavior; this document explains implementation organization; plans describe
sequencing rather than requirements.
