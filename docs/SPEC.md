# Local Face Recognition Project

## 1. Purpose and scope

Build a completely local macOS application for indexing photographs,
detecting faces, identifying people, and searching a photo collection.

The application is primarily a CLI in the MVP. Its application services and
data model must remain independent of the CLI so that a future GUI can be
added without changing the core architecture.

### 1.1 MVP scope

- macOS on Apple Silicon.
- Local directories, scanned recursively.
- Still images only.
- No RAW formats in the MVP.
- No video processing in the MVP.
- Live Photos contribute only their still-image component.
- Apple Photos is not used or integrated.
- No cloud-based face recognition or runtime cloud processing.
- Search by person, directory, and capture date.
- Multiple configured collection roots.
- Exact duplicate file content is analyzed once but remains visible at every
  discovered filesystem path.
- Discovery-first identity review: photos can be processed without any
  pre-existing reference faces, with unfamiliar faces grouped for review.

The initial supported still-image format list is expected to include JPEG,
PNG, HEIF/HEIC, TIFF, and WebP. This list must be confirmed against the
chosen Python image-decoding stack before implementation.

### 1.2 Explicit non-goals for the MVP

- RAW image support.
- Video frame extraction or video tracking.
- Apple Photos integration.
- Cloud processing, telemetry, or remote search.
- Automatic identity assignment without user confirmation.
- User-facing person merge/split workflows.
- A GUI.

## 2. Design principles

- Go owns the application.
- Python owns ML operations only.
- SQLite is the system of record.
- Original photographs are never modified.
- A file-content asset is distinct from its filesystem paths.
- Detection, analysis, recognition, and identity assignment are separate
  concepts.
- Unknown is a valid and expected result.
- Discovery and review are the normal path for unfamiliar faces; reference
  faces are accumulated through confirmed user decisions over time.
- Model and preprocessing provenance is retained with derived data.
- Background processing may be paused, resumed, throttled, or restarted
  without changing results.
- All runtime processing is local and does not require network access.

## 3. System architecture

The application consists primarily of a Go application and a small Python ML
worker.

### 3.1 Go responsibilities

- CLI and application lifecycle.
- Collection configuration.
- Directory scanning and incremental file discovery.
- File identity and SHA-256 hashing.
- EXIF and photo metadata extraction.
- SQLite persistence and migrations.
- Photo assets and filesystem paths.
- People management.
- Face records and embedding storage.
- Similarity matching and recognition decisions.
- Identity history and user confirmation.
- Search.
- Scan, analysis, recognition, and clustering job orchestration.
- Worker startup, health, restart, timeout, and cancellation handling.
- Resource throttling and user-controlled processing modes.

### 3.2 Python responsibilities

- InsightFace model loading.
- Image decoding required by the selected ML stack.
- EXIF orientation handling for inference.
- Face detection.
- Face alignment.
- Embedding generation.
- Face-quality and optional analysis metrics.
- Experimental clustering.

Python must not own:

- SQLite.
- Photo metadata persistence.
- People.
- Identity assignments.
- Application state.
- Job state.

### 3.3 Replaceable ML boundary

Go should define an abstraction similar to:

```go
type Recognizer interface {
    Analyze(ctx context.Context, path string) ([]DetectedFace, error)
}
```

The initial implementation is an InsightFace Python worker. A future
implementation may invoke ONNX models directly and eliminate the Python
dependency.

## 4. Collections, assets, and paths

A collection is a configured root directory. Each collection has a stable ID
and is scanned recursively.

The logical relationship is:

```text
collection
  └── photo_path ── photo_asset
                         └── analysis
                               └── face ── embedding
```

### 4.1 Asset identity

- A photo asset is identified by the complete file-content SHA-256 hash.
- The same content hash maps to one asset.
- Exact duplicates are deduplicated regardless of path or collection.
- Visually similar but byte-different files are distinct assets.
- A changed file at an existing path becomes a new asset reference.
- A renamed or moved file remains the same asset if its content is unchanged.

### 4.2 Path records

Every discovered supported-image path is represented separately, even when
multiple paths refer to one asset.

Path records contain, at minimum:

- collection ID
- relative path within the collection
- current/missing state
- file size
- modification timestamp
- first-seen and last-seen timestamps
- referenced asset ID

Directory search operates on path records. Face and embedding analysis
operates on assets.

### 4.3 Collection rules

- Collection roots are scanned recursively.
- Symlinks are ignored in the MVP.
- Overlapping collection roots are rejected or reported as configuration
  errors before scanning.
- Missing paths are retained by default.
- An explicit prune operation may remove missing paths and unreferenced
  derived data.
- The same asset may be referenced by paths in multiple collections.

## 5. Filesystem scanning and incremental discovery

A scan must:

1. Enumerate supported files below a collection root.
2. Ignore unsupported files.
3. Detect files that are still being copied or modified.
4. Hash stable candidate files.
5. Find or create the content asset.
6. Extract canonical image metadata.
7. Create or update the path record.
8. Schedule analysis only when the required pipeline analysis is absent or
   explicitly stale.
9. Mark paths not discovered during the scan as missing.

### 5.1 File stability

The scanner should compare file size and modification time across a stability
check before hashing. Files that change while being hashed or analyzed are
discarded from that attempt and retried later.

### 5.2 Scan resumability

Scan runs and per-item progress are persisted. A process interruption must not
require a full restart. A scan can be paused, resumed, cancelled, or retried.

### 5.3 Metadata ownership

- Embedded image metadata belongs to the deduplicated asset.
- Directory and path belong to the path record.
- Embedded capture date is the canonical capture date when present.
- Filesystem timestamps may be retained separately but are not the primary
  capture date unless embedded capture date is unavailable.

## 6. Go/Python worker contract

The worker is a persistent subprocess. Communication initially uses
newline-delimited JSON over stdin/stdout.

### 6.1 Request

```json
{
  "protocol_version": 1,
  "id": "request-123",
  "command": "analyze",
  "path": "/photos/IMG_1234.jpg",
  "expected_sha256": "...",
  "expected_size": 4821931
}
```

The worker receives a path plus expected file identity. Passing image bytes is
not required for the MVP.

### 6.2 Successful response

```json
{
  "protocol_version": 1,
  "id": "request-123",
  "ok": true,
  "image": {
    "width": 4032,
    "height": 3024
  },
  "model": {
    "pipeline_id": "..."
  },
  "faces": [
    {
      "box": {
        "left": 120,
        "top": 240,
        "right": 620,
        "bottom": 840
      },
      "detection_confidence": 0.997,
      "quality": 0.81,
      "embedding": "..."
    }
  ]
}
```

The embedding representation in the wire protocol may be base64 or another
explicit binary-safe encoding. JSON is not the storage representation.

### 6.3 Error response

```json
{
  "protocol_version": 1,
  "id": "request-123",
  "ok": false,
  "error": {
    "code": "unsupported_image",
    "message": "...",
    "retryable": false
  }
}
```

### 6.4 Contract rules

- stdout contains protocol messages only.
- Diagnostics go to stderr.
- Every response contains the request ID.
- Protocol version is explicit.
- Errors are structured and categorized.
- Face ordering is deterministic, such as top-to-bottom then left-to-right.
- Bounding boxes use original-image pixel coordinates after EXIF orientation.
- Model and preprocessing provenance is returned or negotiated.
- Go validates that the analyzed file still corresponds to the expected asset.
- One request is in flight per worker in the MVP.

### 6.5 Worker failures

Each request has a timeout. On timeout, malformed output, or worker crash:

- Go terminates and restarts the worker.
- The failed item receives a retryable error.
- Retries are bounded and persisted.
- A permanent failure is visible in job status and diagnostics.

Cancellation is implemented by terminating and restarting the worker rather
than using a dedicated cancellation command in the MVP.

## 7. Image preprocessing

The worker should:

1. Open the original file.
2. Decode the supported format.
3. Apply EXIF orientation.
4. Convert to the model-required color format.
5. Resize according to the selected model configuration.
6. Detect faces.
7. Align faces.
8. Generate embeddings and quality metrics.
9. Map boxes to the oriented original-image coordinate system.
10. Return results without creating durable image derivatives.

The application does not modify original files or require stored thumbnails in
the MVP.

## 8. ML pipeline and model provenance

The initial ML pipeline is InsightFace 2.0.0 with the `buffalo_l` model pack,
running under Python 3.11 and ONNX Runtime 1.30.0.

The application uses the trimmed `buffalo_l` configuration with only the
detection and recognition modules enabled:

```python
allowed_modules=["detection", "recognition"]
```

The full model-pack configuration remains available for future features that
need attributes or additional landmark models. `buffalo_s` is not the default
fallback because the trimmed `buffalo_l` configuration met the initial
performance requirements.

InsightFace publishes standard benchmark results for its model packs, but the
application must calibrate behavior on a representative local dataset. Exact
model choice, thresholds, and quality policy remain prototype-dependent.

### 8.1 Pipeline identity

A pipeline ID identifies the complete analysis configuration:

- model family and version
- model-file checksums
- detector and alignment configuration
- preprocessing settings
- embedding dimension
- data type
- normalization procedure
- relevant Python and dependency versions

A change to any of these creates a new pipeline ID.

The validated initial pipeline records Python 3.11, InsightFace 2.0.0, ONNX
Runtime 1.30.0, `buffalo_l`, the trimmed module list, model-file checksums,
detector size, and the selected execution provider.

### 8.2 Analysis versioning

An asset may have one analysis per pipeline:

```text
asset
  └── analysis(pipeline_id)
        └── detected faces
              └── embedding
```

Existing analyses are retained when a pipeline changes. Recognition compares
only compatible pipelines.

## 9. Embeddings

- Store the complete model-produced embedding.
- Store it as binary float32 data, not JSON.
- Record dimension, data type, pipeline ID, and normalization state.
- Preserve the full vector even if normalized vectors are used for comparison.
- Provide export/debug support that can emit vectors as JSON when needed.

Binary storage preserves the numeric vector while reducing database size and
parsing overhead. JSON is an interchange/debug format, not the primary
database representation.

## 10. Recognition and identity assignment

### 10.1 Matching

Go performs similarity matching. The initial metric is cosine similarity over
compatible embeddings.

The initial matcher compares a query face against confirmed examples when
examples are available and uses the strongest match plus top-k supporting
evidence for each person. A later vector index may be added if benchmarks
require it.

Matching is not required for initial processing. A collection may contain no
people or no confirmed examples when it is first scanned. In that case, faces
are retained as unidentified and may be grouped into provisional clusters for
user review.

### 10.2 Decision inputs

Recognition decisions may consider:

- absolute similarity
- margin over the second-best person
- number of supporting confirmed examples
- face quality
- detection confidence

Threshold values are calibrated from labeled data and remain configurable.

The cold-start policy permits a person with one confirmed example to produce a
lower-confidence suggestion. Multiple independent confirmed examples can
produce stronger evidence.

### 10.3 Assignment policy

- The MVP never silently assigns an identity.
- High-confidence results are suggestions.
- Users explicitly confirm or reject suggestions.
- Ambiguous or low-confidence results remain unknown.
- Manual assignment is allowed even when the model provides no suggestion.
- Confirmed and manually assigned faces become future matching examples.
- Rejected suggestions never become matching examples.
- Rejected suggestions are remembered to avoid repeated presentation.

### 10.4 Discovery-first workflow

Discovery-first is the normal application workflow, not an exceptional
fallback. A user can scan and analyze photos without first creating people or
selecting reference faces.

The workflow is:

1. Detect faces and generate embeddings for all supported photos.
2. Match faces against existing confirmed examples where possible.
3. Group unfamiliar or low-confidence faces into provisional clusters.
4. Let the user review clusters and individual faces, name a person, assign a
   face to an existing person, mark a face unknown, or reject a false
   detection.
5. Treat confirmed and manually assigned faces as future examples for that
   person.

Provisional clusters never create confirmed identities by themselves. A
cluster may contain faces from more than one person and must remain explicitly
reviewable. The initial clustering algorithm and its parameters are deferred,
but a basic grouping implementation is required for the MVP review workflow.

The application should distinguish at least these states during review:

- known person
- genuinely unknown person
- ambiguous or unusable face
- false detection
- missed face, recorded during review when applicable

### 10.5 Assignment history

Identity decisions are historical records, not a single mutable
`face.person_id` field. Records include suggestion, confirmation, rejection,
manual assignment, and revocation events.

A face may have at most one current confirmed person, while retaining prior
events. The current identity is a derived query/view over the history.

## 11. Clustering

Clustering is performed after analysis as part of the discovery-first review
workflow. It may run automatically as a post-scan job or be explicitly
requested, but it is never an identity assignment.

- It operates on all unknown faces or a user-selected subset.
- It never silently creates or assigns identities.
- Bulk assignment requires explicit confirmation.
- A clustering run stores its input set, pipeline, parameters, algorithm, and
  timestamp.
- Users may inspect, accept, and reject provisional clusters in the MVP, and
  may assign individual faces from a cluster. User-facing merge and split
  operations remain deferred to a later version.
- The exact clustering algorithm and parameters are deferred until a
  prototype dataset is available, but the clustering boundary and review
  behavior are MVP requirements.

## 12. Data model

Initial logical entities:

- `collections`
- `photo_assets`
- `photo_paths`
- `analyses`
- `faces`
- `embeddings`
- `people`
- `identity_events` or `identity_assignments`
- `scan_runs`
- `scan_items`
- `clustering_runs`
- provisional cluster and cluster-membership records
- `schema_migrations`
- `pipelines`

### 12.1 Invariants

- One content hash maps to one asset.
- Multiple path records may reference one asset.
- An asset may have one analysis per pipeline.
- Faces belong to analyses.
- Embeddings belong to faces and inherit pipeline provenance.
- A face has at most one current confirmed identity.
- A rejected suggestion is not a matching example.
- Revoked assignments are not matching examples.
- Deleting a path does not delete an asset referenced by another path.
- Asset deletion requires no remaining path references and is explicit.

### 12.2 People lifecycle

The data model supports merge and split history. User-facing merge/split
operations are deferred until after basic people management and confirmation
workflows are working.

People are not physically deleted automatically. A merge preserves history;
a split creates a new person and moves selected current assignments while
retaining the audit trail.

## 13. Storage and migrations

Use one SQLite database with logical collections rather than one database per
collection. This supports global people, cross-collection search, and
cross-collection deduplication while retaining collection-specific queries.

### 13.1 Schema migrations

- Store the schema version in a migration table.
- Migrations are sequential and immutable after application.
- Apply schema migrations transactionally.
- Automatically create a backup before migrations.
- Run migrations automatically at startup.
- Provide a `--no-migrate` safety option.
- Validate post-migration invariants.
- Refuse to run when the database is newer than the application supports.
- Support the latest schema after successful upgrade rather than indefinite
  compatibility with all historical schemas.

Large index rebuilds and derived-data changes should be resumable maintenance
jobs rather than long, unobservable startup migrations.

### 13.2 Model changes

Model changes create new pipeline analyses and do not overwrite old
embeddings. Reprocessing is resumable, cancellable, and recorded as a job.
Old identity assignments remain historical and may be marked for review.

### 13.3 Export and backup

Exports record schema, application, and pipeline versions. Export format
versioning is independent of SQLite schema versioning. The application must
support database backup, metadata export, identity-assignment export, optional
embedding export, and purge of derived data.

## 14. CLI and workflows

The MVP is CLI-first. Exact command names may evolve, but the CLI must support
these workflows:

- add/list/remove collections
- scan, scan status, pause, resume, cancel
- search by person, directory, and capture-date range
- create/list/rename people
- inspect unknown faces
- inspect provisional face clusters
- inspect, confirm, reject, and manually assign suggestions
- run and inspect clustering jobs
- inspect application and worker status
- configure processing and model settings
- export and purge derived data

Commands support human-readable output by default and a consistent structured
JSON output mode for scripts. Exit codes distinguish success, partial failure,
invalid input, and interruption.

Identity-modifying commands require interactive confirmation by default.
Explicit non-interactive/batch mode is supported.

## 15. Background processing and resource policy

The application supports:

- automatic adaptive mode
- full-speed mode
- reduced or paused mode

Automatic mode uses available system signals as heuristics, including recent
user activity, system load, thermal pressure, power state, and application
activity. When the computer appears idle, background work may use available
resources. When the computer is being used, concurrency and processing
intensity are reduced.

Manual controls are available when presence detection is unavailable or
unreliable. Throttling affects throughput only; it must not affect result
correctness.

## 16. Performance targets

The MVP is designed around collections of approximately 100,000 images.
Correctness and resumability for larger collections are desirable; one million
images is a later scalability target rather than an MVP requirement.

The implementation must benchmark:

- first scan
- unchanged rescan
- duplicate-heavy collections
- analysis throughput
- recognition latency
- database size
- memory usage
- worker startup and recovery
- adaptive throttling

### 16.1 Initial benchmark results

The baseline was tested on the same 86-image, 129-face dataset on an Apple
Silicon M1 with 8 GB RAM and an Apple Silicon M4. Both machines used the
CoreML execution provider with CPU fallback, and both completed all images
without errors.

For the trimmed `buffalo_l` configuration:

- M1: 18.80 images/second and 28.20 faces/second.
- M4: 29.32 images/second and 43.98 faces/second.
- Peak RSS: approximately 1.19 GiB on M1 and 1.54 GiB on M4.
- All 129 face counts and bounding boxes matched between machines.
- Cross-machine embedding cosine similarity was at least
  `0.999999999999982`.
- Full and trimmed configurations produced byte-identical embeddings on each
  machine for all 129 faces.

The trimmed configuration improved warm throughput by approximately 18% on
the M1 and 13% on the M4, but did not materially reduce peak memory. These
results establish the configuration as the initial baseline, not as a
guarantee for all future macOS or Apple Silicon systems.

Ordinary searches should remain interactive for the primary target collection.
Initial matching may scan all eligible confirmed embeddings. A vector index or
person-level representative vectors may be introduced if benchmarks justify
them, while retaining exact matching as a reference path.

## 17. Packaging and runtime dependencies

- Do not bundle a managed Python runtime.
- Do not require a bundled application installation for the MVP.
- System-installed Python 3.11 or a separately installed Python 3.11 is the
  default prototype/runtime prerequisite.
- Setup creates a Python virtual environment.
- Python dependencies are pinned and installed during setup.
- Model files are downloaded during setup, verified by checksum, and recorded
  by pipeline ID.
- The initial setup pins InsightFace 2.0.0 and ONNX Runtime 1.30.0.
- Runtime processing works without network access.
- Docker configuration may be provided as an optional reproducible environment,
  but is not the default macOS runtime.
- Code signing and notarization are release-hardening work after packaging
  stabilizes.

The prototype pins Python 3.11, InsightFace 2.0.0, and ONNX Runtime 1.30.0.
The final dependency-locking mechanism must be confirmed during the
clean-machine setup prototype.

## 18. Privacy and security

- No network access is required for scanning, analysis, recognition, clustering,
  or search.
- No telemetry is collected.
- Embeddings are treated as sensitive biometric-derived data.
- Logs do not include image contents or embeddings.
- Full image paths and metadata are minimized in logs unless diagnostic logging
  is explicitly enabled.
- Original images are never modified.
- Temporary files are minimized and cleaned up.
- Users can export or delete derived face data.
- Database backups and exports are treated as sensitive data.
- Model and dependency downloads occur only during explicit setup/install
  activity.

## 19. Testing and evaluation

### 19.1 Unit and contract tests

- hashing and duplicate detection
- EXIF orientation and coordinate conversion
- metadata extraction
- threshold and support-count calculations
- assignment state transitions
- SQLite constraints and migrations
- worker request/response schemas
- malformed worker output and structured errors

### 19.2 Integration tests

- worker startup, shutdown, timeout, and restart
- interrupted and resumed scans
- file changes during scanning
- duplicate paths sharing one asset
- missing and restored paths
- pipeline changes and reprocessing
- export and purge behavior
- adaptive processing controls

### 19.3 End-to-end fixtures

Fixtures should include duplicates, renamed files, multiple faces, rotated
images, unsupported files, corrupt images, missing paths, known identities,
unknown faces, and ambiguous matches.

### 19.4 Recognition evaluation dataset

The project must include instructions for creating a private, local evaluation
dataset from a user-provided collection. The instructions must cover:

- stable person labels
- unknown and ambiguous faces
- difficult poses, blur, occlusion, lighting, and group images
- development, calibration, and final evaluation splits
- duplicate and near-duplicate exclusion across splits
- provenance and consent
- summary statistics without publishing images

Private images, embeddings, and identifying metadata must not be committed to
source control. A public sample dataset may be created later from
appropriately licensed images.

The normal application workflow does not require this dataset structure. The
reference/evaluation separation exists to measure recognition settings without
using the answer as part of the evidence. The simpler human-review procedure
for this purpose is documented in
`docs/protocols/identify_model_settings-simplified.md`.

Recognition evaluation measures false positives, false negatives, unknown
rejection, precision/recall, similarity distributions, support-count effects,
quality effects, and performance across difficult conditions. Thresholds are
not tuned on the final evaluation split.

### 19.5 Packaging tests

On a clean Apple Silicon machine, verify virtual-environment creation,
dependency installation, model download and checksum verification, interrupted
setup recovery, offline runtime, and the optional Docker workflow.

## 20. Model-selection prototype

The initial prototype used the following process:

1. Start with Python 3.11, InsightFace 2.0.0, ONNX Runtime 1.30.0, and
   `buffalo_l`.
2. Run it on a modest private labeled dataset.
3. Calibrate similarity, quality, and supporting-example thresholds.
4. Measure false positives, false negatives, unknown rejection, startup,
   throughput, and memory.
5. Compare full and detection-plus-recognition-only `buffalo_l` configurations.
6. Adopt the trimmed configuration after confirming equivalent outputs and
   improved throughput.

The prototype must record model and model-file versions, checksums,
preprocessing, alignment, normalization, Python dependencies, hardware, and
operating-system details.

The current use case is personal/non-commercial. Any future distribution
beyond that scope must re-evaluate the InsightFace model license and
redistribution requirements.

## 21. Failure handling and recovery

- Per-file errors do not abort an entire scan.
- Unsupported and corrupt files receive categorized outcomes.
- Worker failures are retried with bounded retries.
- Interrupted jobs resume from persisted progress.
- Database migrations are transactional and backed up.
- Files changing during processing are retried.
- Partial completion is visible through status commands.
- Failed items remain inspectable and can be retried explicitly.

## 22. Open decisions and prototype gates

The following are intentionally not fixed until prototype evidence exists:

- final supported still-image format list
- final dependency lock format
- exact production dependency update policy
- detection and quality thresholds
- recognition thresholds and support-count policy values
- face-quality implementation
- clustering algorithm and parameters
- final performance numbers
- database location and backup defaults
- final CLI command names and output schemas
- code-signing and notarization requirements

## 23. Implementation gates

Implementation must not begin until this specification is explicitly approved.

Before implementation approval, the project should have:

1. A confirmed scope and data model.
2. A worker protocol definition.
3. Migration and versioning rules.
4. A model-selection and dataset-creation plan.
5. Defined privacy and packaging constraints.
6. Initial acceptance criteria for scanning, analysis, recognition, search,
   and recovery.

## Current phase

DESIGN ONLY.

No implementation has been authorized by this document. The next design
activities are to finalize the prototype plans, confirm remaining open
operational details, and obtain explicit approval of this specification.
