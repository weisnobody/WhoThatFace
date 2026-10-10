# Implement the Face-Me-Bro MVP

## Summary

Build the MVP as a Go application with:

- SQLite as the initial database, behind a database/repository boundary that remains PostgreSQL-ready.
- A persistent Python InsightFace worker for detection, embeddings, quality, and detection confidence.
- Go-owned scanning, recognition, clustering, jobs, migrations, search, authentication, and review workflows.
- A Go `net/http` web service using server-rendered HTML plus focused vanilla JavaScript.
- A CLI sharing the same application services as the web UI.

The existing model-selection and review prototypes remain validation references, not production code.

## Implementation phases

### 1. Project foundation

Create the production Go module and package boundaries:

- `cmd/face-me-bro`
- `internal/app`
- `internal/config`
- `internal/db`
- `internal/jobs`
- `internal/media`
- `internal/recognition`
- `internal/clustering`
- `internal/review`
- `internal/auth`
- `worker/`

Use:

- `database/sql`;
- pure-Go SQLite for the MVP;
- embedded, ordered SQL migrations;
- `net/http`, `html/template`, and embedded static assets;
- Argon2id password hashing;
- server-side opaque sessions stored in SQLite.

Keep SQL in a portable subset suitable for later PostgreSQL support. PostgreSQL is not implemented in the MVP, but repositories and service interfaces must not expose SQLite-specific behavior.

### 2. Configuration and runtime setup

Implement:

- default macOS data root: `~/Library/Application Support/face_me_bro/`;
- alternate configuration file/location CLI option;
- configuration-controlled runtime data root;
- database, model, log, temporary, export, and backup directories;
- setup validation for Python 3.11, virtual environment, dependencies, model files, and checksums;
- status output showing resolved paths, pipeline, provider, and database.

Create `requirements.in` and generated pinned lock files for the worker environment.

### 3. Database and migrations

Implement the schema for:

- collections and collection roots;
- assets and path records;
- analyses, faces, embeddings, and pipelines;
- people and provisional people;
- assignment events and current assignment projections;
- reference recommendations and reference-selection events;
- clustering runs, clusters, memberships, review events, and dispositions;
- jobs and job items;
- accounts, collection permissions, sessions, and audit events.

Required invariants:

- content hashes deduplicate assets;
- paths remain independently searchable;
- one analysis exists per asset/pipeline;
- events are immutable;
- current-state projections update transactionally;
- user-confirmed assignments are protected from automatic reassignment;
- current approved references alone enter the matching gallery.

Implement migration backups, `--no-migrate`, newer-schema refusal, invariant checks, backup/restore, and projection rebuild validation.

### 4. Media scanning and analysis pipeline

Implement recursive scanning with:

- supported JPEG, PNG, HEIF/HEIC, TIFF, and WebP handling;
- Live Photo/motion-photo still extraction;
- full source-file hashing;
- file stability checks;
- duplicate asset reuse;
- per-path filesystem timestamps;
- asset-level EXIF metadata;
- capture-date fallback: EXIF, creation time, modification time, null;
- missing-path retention;
- unsupported/corrupt-file categorization.

Implement the NDJSON worker protocol:

- startup capability handshake;
- major/minor protocol negotiation;
- Base64 little-endian float32 embeddings;
- explicit metric and provenance validation;
- successful no-face responses;
- structured retryable/non-retryable errors;
- resource-limit reporting;
- one image request per worker;
- worker restart and bounded retry behavior.

Persist source and inference dimensions, oriented boxes, quality, detection confidence, embeddings, provider, and pipeline provenance.

### 5. Recognition and evidence evaluation

Implement Go recognition over approved reference faces using cosine similarity.

Support:

- similarity and margin;
- support count as approved reference-face count;
- per-person candidate evaluation;
- standalone matching defaults when clustering is disabled;
- clustering evidence categories when clustering is enabled;
- missing metrics as `0.0`;
- `either` metrics;
- high evidence requiring every applicable high criterion;
- medium evidence requiring every applicable metric to be at least medium;
- low evidence when any metric is low;
- similarity below `0.25` requiring manual review;
- disagreement requiring cluster review.

Implement assignment states:

- `unassigned`;
- `provisional`;
- `assigned-system`;
- `assigned-user`;
- `unknown`;
- `ambiguous`.

Implement independent dispositions:

- `incorrect-assignment`;
- `false-detection`;
- `dont-track`.

Persist all evidence snapshots and state transitions as events.

### 6. Go clustering

Implement the initial grouping algorithm in Go:

1. Select all eligible faces by default.
2. Support unresolved-only and explicit-subset modes.
3. Perform exact batched cosine nearest-neighbor search.
4. Construct mutual-kNN edges.
5. Apply `k=40` and face-to-face threshold `0.45` defaults.
6. Build connected components.
7. Persist immutable clustering runs and memberships.
8. Derive purity/connectivity, agreement, mixed-cluster, and review flags.

Define an internal neighbor-search interface so ANN can replace exact search later without changing cluster semantics.

Implement active/superseded runs, old-run viewing, superseded-run warnings, and carry-forward rules.

### 7. Reference recommendations and reprocessing

Implement recommendations for assigned-system and assigned-user faces, including assigned don’t-track faces.

Rank using:

- quality;
- detection confidence;
- similarity;
- margin;
- support;
- cluster purity/connectivity;
- distance from existing references;
- duplicate avoidance;
- diversity across photos, dates, clusters, poses, and lighting.

Display up to 20 current candidates per person. Store every recommendation generation and every approval/rejection.

When a reference changes:

- reprocess faces considering the affected person;
- reprocess affected clusters;
- exclude assigned-user identities from automatic reassignment and cluster changes;
- refresh recommendations and search indexes for all eligible faces;
- process work by the defined priority queue.

### 8. CLI

Implement command families for:

- collections;
- scan/jobs;
- people;
- faces;
- clusters;
- search;
- configuration/status;
- export/backup;
- accounts and collection access;
- password reset;
- audit purge.

Provide human-readable output and versioned JSON output with metadata, results, warnings, and structured errors.

Require explicit confirmation for non-interactive identity changes.

### 9. Web review service

Implement authenticated local accounts with:

- bootstrap `admin` / `admin`;
- mandatory first-login password change;
- Argon2id password hashes;
- one-week sessions;
- secure HTTP-only same-site cookies;
- CSRF protection;
- lockout after 10 failures in 30 minutes;
- administrator disable/re-enable;
- collection-level permissions;
- revocable undo permission;
- audit history and age-based purge.

Implement review pages for:

- photos and face boxes;
- people and provisional people;
- clusters;
- singleton virtual review groups;
- mixed clusters;
- references and recommendations;
- search/filter state controls;
- old clustering runs.

Use authorized application endpoints for all images, crops, paths, and metadata. Never expose raw filesystem paths.

### 10. Jobs and adaptive processing

Implement persisted jobs for:

- scans;
- analysis;
- recognition;
- clustering;
- reference-change reprocessing;
- maintenance;
- index rebuilds.

Support pause, resume, cancel, retries, progress, partial failure, and recovery.

Implement automatic/full-speed/reduced-paused processing modes with manual override. Throttling must not affect results.

## Testing and acceptance

Add unit, integration, and end-to-end tests covering:

- migrations, backups, projection rebuilds;
- duplicate assets and multiple paths;
- file stability and missing paths;
- capture-date precedence;
- worker protocol validation and restart;
- no-face and malformed responses;
- embedding decoding;
- assignment transitions and undo conflicts;
- reference approval and reprocessing;
- clustering metrics and mixed clusters;
- old-run warnings and carry-forward;
- search filters;
- account lockout, permissions, CSRF, and audit;
- authorized image access and path traversal prevention;
- CLI human/JSON output;
- interrupted and resumed jobs.

Acceptance requires processing the target local collection, completing discovery-first review, creating references, reprocessing affected faces, searching by person/directory/date, and recovering cleanly from worker/process interruption.

## Assumptions

- macOS Apple Silicon is the initial supported platform.
- Go implements clustering.
- SQLite is the MVP database; PostgreSQL support is a future backend.
- The web UI uses Go templates and vanilla JavaScript.
- Original photos remain local and are never modified or uploaded by default.
- ANN, large-library performance targets, recommendation optimization, and signing/notarization remain later validation work.
