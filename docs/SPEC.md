# Local Face Recognition Project

## 1. Purpose and scope

Build a completely local macOS application for indexing photographs,
detecting faces, identifying people, and searching a photo collection.

The application provides a CLI and an authenticated web review service in the
MVP. Application services and the data model must remain independent of both
presentation layers.

### 1.1 MVP scope

- macOS on Apple Silicon.
- Local directories, scanned recursively.
- Still images only.
- No RAW formats in the MVP.
- No video processing in the MVP.
- iOS Live Photos and Android-style motion photos contribute only their
  reference still-image component; the motion/video component is ignored in
  the MVP.
- Apple Photos is not used or integrated.
- No cloud-based face recognition or runtime cloud processing.
- Search by person, directory, and capture date.
- Multiple configured collection roots.
- Exact duplicate file content is analyzed once but remains visible at every
  discovered filesystem path.
- Discovery-first identity review: photos can be processed without any
  pre-existing reference faces, with unfamiliar faces grouped for review.

The initial supported still-image format list is JPEG, PNG, HEIF/HEIC, TIFF,
and WebP, subject to compatibility testing with the chosen Python image
decoding stack. Live Photo and motion-photo containers are supported only when
the still-image component can be extracted reliably. The motion or video
component is not processed in the MVP.

### 1.2 Explicit non-goals for the MVP

- RAW image support.
- Video frame extraction or video tracking.
- Animated-image frame extraction or animation tracking.
- Apple Photos integration.
- Cloud processing, telemetry, or remote search.
- Automatic promotion to a user-confirmed identity without user confirmation.
- User-facing person merge/split workflows.
- A native desktop GUI. The MVP web review service is included separately.

## 2. Design principles

- Go owns the application.
- Python owns ML operations only.
- SQLite is the system of record.
- Original photographs are never modified.
- A file-content asset is distinct from its filesystem paths.
- Detection, analysis, recognition, and identity assignment are separate
  concepts.
- Unknown is a valid and expected result.
- Discovery and review are the normal path for unfamiliar faces; confirmed
  faces become eligible for reference recommendations, and reference faces
  are accumulated through explicit user approval.
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
- Face-to-face similarity search and clustering. The initial grouping
  implementation is preferred in Go; a Python-worker implementation is
  acceptable if required by the selected libraries or prototype results.
- Identity history and user confirmation.
- Search.
- Scan, analysis, recognition, and clustering job orchestration.
- Authenticated web review service, collection access control, and review
  audit history.
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
- For motion-photo containers, the hash covers the complete source file, not
  only the extracted still-image pixels.
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
- media kind (`still`, `live_photo`, or `motion_photo`), when applicable
- motion-photo container/pair information and the still component used for
  analysis

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
- Capture date selection uses this order: embedded EXIF data, filesystem
  creation time, filesystem modification time, then null.
- The selected capture date and its source are retained so users can determine
  why a date was chosen.
- If a filesystem does not provide a reliable creation time, the application
  uses whatever creation date the filesystem reports or continues to the next
  fallback; if no timestamp is available, capture date is null.
- For supported Live Photo and motion-photo containers, metadata and analysis
  use the reference still image; motion/video metadata is not required in the
  MVP.
- A supported Live Photo or motion photo is one user-visible path/asset record.
  The record retains the container or pair format, still component source,
  companion motion/video component when present, and extraction
  method/version. Companion motion/video files are not separate
  face-analysis assets. An unassociated or unsupported companion is ignored as
  unsupported input.

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
      "embedding": {
        "encoding": "base64",
        "dtype": "float32",
        "byte_order": "little",
        "dimension": 512,
        "normalized": true,
        "data": "..."
      }
    }
  ]
}
```

Embeddings use standard Base64 containing contiguous little-endian float32
values. The response includes the encoding, dtype, byte order, dimension, and
normalization state. Go validates that the decoded length equals
`dimension * 4`; JSON is not the storage representation.

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
- `detection_confidence` and `quality` are required numbers in `[0.0, 1.0]`.
- NaN, infinity, malformed boxes, invalid dimensions, and embedding length
  mismatches are protocol/data errors.
- Optional metrics are explicitly nullable rather than ambiguously omitted.
- The worker does not return an informal `signal` metric without a defined
  calculation and version.

### 6.5 Worker capabilities and protocol compatibility

At startup, the worker sends capabilities containing the protocol version,
pipeline ID, model family/version, model-file checksums, enabled modules,
detector configuration, preprocessing/orientation behavior, embedding
properties, execution provider and version, supported commands, and resource
limits. Go validates these capabilities against the requested pipeline before
submitting analysis.

Protocol versions have explicit major and minor components. Unsupported major
versions are rejected; compatible minor versions may be negotiated. Requests
and responses contain the negotiated version and request ID. Unknown response
fields are ignored for forward compatibility, while missing required fields or
pipeline mismatches fail before analysis begins. Protocol changes require
contract tests and documented compatibility rules.

### 6.6 Analysis result rules

An image with no detected faces returns a successful response with
`"faces": []`, image dimensions, pipeline ID, and provenance. It is persisted
as a completed no-face analysis rather than an error. Decode failures,
unsupported formats, and resource-limit failures are structured errors.

Live Photo and motion-photo handling occurs before face analysis. A media
adapter identifies or extracts the reference still image, and the worker
receives that still image for normal analysis. The original collection path
remains the single user-visible asset/path record, while provenance records the
container type, still component, and extraction method/version. Failure to
identify or extract the still component is an unsupported-format result.

Maximum decoded pixels and width/height are configurable safety limits. The
worker reports source and inference dimensions and maps boxes back to oriented
source-image coordinates. Exceeding a hard limit returns a non-retryable
`resource_limit` error containing the source dimensions and configured limit.
The MVP uses one image per request; protocol-level batching is deferred.

### 6.7 Worker failures

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

The initial matcher compares a query face against approved reference faces
when references are available and uses the strongest match plus top-k
supporting evidence for each person. A later vector index may be added if
benchmarks require it.

Matching is not required for initial processing. A collection may contain no
people or no approved references when it is first scanned. In that case, faces
are grouped into provisional clusters. The system may create an unnamed
provisional person record as the candidate/association target for a group until
the user confirms, renames, rejects, or otherwise disposes of it.

When clustering is not being used, the standalone matcher uses a similarity
threshold of `0.25` and margin of `0.05` for people with approved references,
and a similarity threshold of `0.40` and margin of `0.05` for faces without
approved references when evaluating a provisional person/candidate. These are
configurable matching defaults; they are distinct from the clustering evidence
categories in Section 10.2.

### 10.2 Decision inputs

Recognition decisions may consider:

- absolute similarity
- margin over the second-best person
- number of supporting approved reference faces
- face quality
- detection confidence

Threshold values are configurable and may be recalibrated from labeled data.
When clustering is disabled, the standalone recognition matcher uses the
configured similarity and margin defaults for people with and without approved
references. When clustering is enabled, the evidence categories below are
used to evaluate a face against each candidate person; the categories combine
recognition and grouping evidence.

The initial production evidence categories are defined as follows. High
evidence requires every applicable non-`either` criterion to be high. Medium
evidence requires every applicable non-`either` criterion to be at least
medium. A category is based on the applicable row values; `either` means that
the evidence may be present or absent without determining that category by
itself. The thresholds are independently configurable.

| Evidence | High | Medium | Low |
| --- | --- | --- | --- |
| Approved references | true | either | either |
| Similarity | >= 0.50 | >= 0.40 | >= 0.25 |
| Margin | >= 0.05 | >= 0.05 | >= 0.05 |
| Supporting examples | >= 5 | >= 3 | < 3 |
| Face quality | >= 0.75 | >= 0.60 | < 0.60 |
| Detection confidence | >= 0.75 | >= 0.60 | < 0.60 |
| Cluster purity/connectivity | >= 0.95 | >= 0.90 | < 0.90 |
| Recognition/grouping agreement | true | either | either |

Similarity below 0.25 is outside the low-evidence range and requires manual
review. Missing metrics are treated as `0.0`. `Either` means that the metric
does not affect the category whether its value is true or false. One low
metric lowers the overall evidence to the level of that metric. Recognition
and grouping disagreement requires cluster review. Purity and connectivity
use the definitions established by the grouping benchmark protocol. Support is
the number of approved reference faces for the candidate person. Thresholds
are evaluated independently for each candidate person being considered for a
face, not once globally for the face. High evidence may produce an
`assigned-system` suggestion, but medium and low evidence require user review
and confirmation before a person assignment is treated as user-confirmed. The
system retains all component values used for each decision.

The cold-start policy permits a person with one approved reference to produce
a lower-confidence suggestion. Multiple independent approved reference faces
produce stronger support evidence.

### 10.3 Assignment policy

- The MVP never silently assigns an identity.
- High-confidence results are suggestions.
- Users explicitly confirm or reject suggestions.
- Low-confidence results remain provisional or unknown and require review;
  ambiguous is an explicit user state.
- Manual assignment is allowed even when the model provides no suggestion.
- Confirmed and manually assigned faces become eligible for reference
  recommendations.
- Only faces explicitly approved by the user as references enter the matching
  gallery.
- Rejected suggestions never become matching examples.
- Rejected suggestions are remembered to avoid repeated presentation.

Each face has an assignment state with one of these values:

- `unassigned`: the system cannot decide on one person and the user has not
  assigned one.
- `provisional`: the system suggests one person, but the assignment is not
  user-confirmed.
- `assigned-system`: the system has sufficient confidence that the face is
  that person, but the user has not confirmed it.
- `assigned-user`: the user assigned the face to that person.
- `unknown`: recognition and grouping did not produce an acceptable person
  candidate.
- `ambiguous`: the user cannot determine who the face belongs to.

Face-level dispositions are tracked independently from assignment state:

- `incorrect-assignment`: the face is real, but the system's proposed person
  is incorrect;
- `false-detection`: the detected region is not a usable face;
- `dont-track`: the user does not want the face/person tracked.

Mixed-cluster membership and needs-review are derived flags, not assignment
states. `incorrect-assignment` is recorded as a user rejection/disposition and
does not imply that the detected face itself was invalid.

Reference status is tracked independently with two boolean fields:

- `recommended`: the system currently recommends the face as a reference;
- `reference`: the user selected the face as a reference.

An assignment state is not itself reference status. In particular, a
user-confirmed face may be recommended but is not used for matching until
`reference` is true.

Assignment-state transitions are:

- A newly detected face starts `unassigned`.
- High recognition and grouping evidence that agree on a person produces
  `assigned-system`.
- Medium recognition and grouping evidence that agree on a person produces
  `provisional`.
- A user assignment of a face or cluster produces `assigned-user`.
- Rejecting an `assigned-system` face without assigning a person returns it to
  its prior state, normally `unassigned` or `provisional`, and records an
  `incorrect-assignment` disposition.
- Rejecting an `assigned-user` assignment without a replacement restores the
  previous state, which may be `unassigned`, `provisional`, or
  `assigned-system`, and records the user disposition.
- If recognition and grouping cannot produce an acceptable candidate, an
  unassigned face becomes `unknown`.
- A user who cannot identify the person may change an unknown or unassigned
  face to `ambiguous`.

All assignment states are searchable, subject to default filters. Don't-track
is independent of assignment state. A face assigned by the system to a person
remains assigned even when that person is marked don't-track; the don't-track
disposition controls visibility and tracking behavior separately.

`recognized/high-confidence`, `provisional person association`, and
`user-confirmed assignment` are display labels for the corresponding
`assigned-system`, `provisional`, and `assigned-user` states. `unconfirmed` is
not a stored assignment state; it is a derived review category.

### 10.4 Discovery-first workflow

Discovery-first is the normal application workflow, not an exceptional
fallback. A user can scan and analyze photos without first creating people or
selecting reference faces.

The workflow is:

1. Detect faces and generate embeddings for all supported photos.
2. Match faces against existing approved reference faces where possible.
3. Group all eligible faces into provisional clusters by default. A configured
   unresolved-only mode may instead group only faces without a high-confidence
   recognition result.
4. Let the user review clusters and individual faces, name a person, assign a
   face to an existing person, mark a face unknown, or reject a false
   detection.
5. Treat confirmed and manually assigned faces as candidates for reference
   recommendation. Only user-approved references become matching examples.

Provisional clusters never create confirmed identities by themselves. A
cluster may contain faces from more than one person and must remain explicitly
reviewable. The MVP grouping implementation uses a mutual-k-nearest-neighbor
graph with connected components, initially configured with `k=40` and a
face-to-face similarity threshold of `0.45`. Both parameters are configurable.

The application should distinguish at least these states during review:

- recognized/high-confidence assignment
- provisional person association
- user-confirmed assignment
- needs-review assignment
- genuinely unknown person
- ambiguous or unusable face
- incorrect assignment
- false detection
- mixed-cluster membership
- don't-track membership
- missed face, recorded during review when applicable

### 10.5 Assignment history

Identity decisions are historical records, not a single mutable
`face.person_id` field. Records include suggestion, confirmation, rejection,
manual assignment, and revocation events.

A face may have at most one current confirmed person, while retaining prior
events. The current identity is a derived query/view over the history.

### 10.6 Reference recommendations

The system generates recommendations only for faces that have been confirmed
or manually assigned to a person. A face-level don't-track disposition does
not prevent a face with a person assignment from being recommended as a
reference; this can improve recognition for that person. An unassigned
don't-track face is not eligible for a person-specific reference
recommendation. Faces with explicit individual assignments remain eligible
even when their cluster is mixed. Unknown, ambiguous, unassigned, false
detections, duplicate or near-duplicate images, and faces with very low
quality, detection confidence, or uncertain person correspondence are not
eligible. Each recommendation is retained as a historical record, including:

- recommendation score and component metrics;
- face quality and detection confidence;
- similarity to the person's reference center, when available;
- cluster identifier and cluster-quality evidence;
- diversity or duplicate-avoidance rationale;
- recommendation timestamp;
- reference-set version used to generate it;
- whether it is the current recommendation;
- user approval or rejection and the corresponding account and timestamp;
- promotion timestamp when the face becomes a reference.

When a new recommendation is generated, the prior recommendation remains
auditable and is marked non-current. Recommendation history does not by itself
make a face eligible for matching; the user must set its reference status.

Recommendations are ranked using face quality, detection confidence,
recognition similarity, support, margin, cluster purity/connectivity, distance
from existing references, and diversity across photos, dates, clusters, poses,
and lighting. Recommendations are advisory and never automatically become
references. The user may approve or reject each recommendation. Approved
references remain references until explicitly unselected. Rejected
recommendations remain historical and non-current, but may be reconsidered if
the policy or evidence changes. The review UI displays up to 20 current
recommendation candidates per person by default.

### 10.7 Don't-track state

Don't-track is available both as a person state and as a face-level state. A
user may mark a named person as don't-track, mark a face without a person as
don't-track, or retain a person assignment while also marking that face as
don't-track. A person-level don't-track flag affects default display and
filtering but does not alter grouping or recognition. Don't-track faces remain
available to grouping, recognition, and search operations, but are excluded
from default lists and search results. Users must explicitly enable an
include-don't-track filter to display them.

In cluster review, the user may choose don't-track as the default disposition
for unassigned faces. Saving that default affects only faces without prior
individual assignments; earlier assignments remain unchanged and exceptions
are shown. Face-level don't-track assignments are saved as individual audit
events. Don't-track faces are not automatically promoted to references, but a
face with a person assignment remains eligible for reference recommendation.

The behavior of don't-track assignments during bulk review remains subject to
the cluster-review rules defined separately.

### 10.8 Reference-change reprocessing

When a user approves a new reference face, the system reprocesses faces that
are currently considering the affected person and clusters affected by those
faces. Faces with `assigned-user` identity assignments are excluded from
automatic identity reassignment and cluster-disposition changes. They remain
eligible for refreshed reference recommendations and search-index updates.

Reference-change work is queued in this priority order:

1. recognition predictions;
2. cluster dispositions;
3. provisional person assignments;
4. reference recommendations;
5. search indexes.

The system retains prior predictions and provisional assignments as history or
marks them stale so results produced before and after the reference change can
be distinguished.

## 11. Clustering

Clustering is performed after analysis as part of the discovery-first review
workflow. It may run automatically as a post-scan job or be explicitly
requested, but it is never an identity assignment.

- By default it operates on all eligible faces in the collection.
- A configurable unresolved-only mode operates only on faces without a
  high-confidence recognition result.
- An explicitly requested run may use a user-selected subset.
- One face/asset analysis is one grouping input regardless of how many paths
  reference the asset.
- Don't-track faces remain eligible for grouping, recognition, and search.
- It never silently creates or assigns identities.
- Bulk assignment requires explicit confirmation.
- A clustering run stores its algorithm and implementation version, `k`,
  face-to-face similarity threshold, neighbor-search method, mutual-edge
  policy, input face-set definition and snapshot, cluster memberships,
  component identifiers, review status, mixed-cluster state, timestamp, and
  superseded/replaced run identifiers.
- Clustering runs use the statuses `queued`, `running`, `completed`, `active`,
  `superseded`, `failed`, and `cancelled`.
- A new run becomes the active run for new pages and operations after it is
  accepted. Existing pages may continue to use the prior run. If a user opens
  an old-run page later, it must clearly state that the page is based on a
  superseded run.
- Users may inspect, accept, and reject provisional clusters in the MVP, and
  may assign individual faces from a cluster. User-facing merge and split
  operations remain deferred to a later version.

### 11.1 Cluster review semantics

Cluster review uses the statuses `unreviewed`, `in-progress`,
`partially-reviewed`, `reviewed`, `needs-review`, and `superseded`.

Opening a cluster does not change its status. Editing a face or selecting a
cluster default marks the review `in-progress`. Saving some dispositions makes
it `partially-reviewed`; saving an explicit disposition for every face makes
it `reviewed`. A new clustering run starts with fresh review statuses. An old
run's matching membership may be shown as a suggestion, but must not
automatically mark a new cluster reviewed.

A clustering run owns immutable cluster memberships. Each cluster stores the
system-proposed person, candidate evidence, mixed-cluster state, and review
status. A user-selected cluster default is retained as a review event for
audit, but does not itself assign every face. Saving the cluster creates
individual face-assignment events for eligible faces. Individual face
assignments are authoritative over the cluster default and system suggestion.
A face may instead be assigned to another person, unknown, ambiguous, or
don't-track without changing other members. Mixed-cluster state is derived
from effective person assignments, and cluster review status is an aggregate
of face dispositions and explicit review actions.

For a pure cluster, bulk confirmation is allowed only when the cluster is not
mixed and all faces have the same proposed person. Saving the confirmation
creates individual assignment events for each affected face, recording the
account and timestamp. A face-level rejection or conflicting assignment
prevents that face from being changed by a bulk operation.

Mixed clusters cannot be bulk-confirmed. The user may still select a default
person for the cluster to reduce repetitive work. The default is an in-page,
unsaved choice until the user clicks Save. Saving applies that default only to
faces on the page that do not already have an individual assignment. Faces
previously assigned to another person are not changed; the page displays a
note identifying those exceptions.

The review UI distinguishes face states visually:

- green border: the face has a saved user-assigned person;
- yellow border: the face is currently using the user-selected cluster default
  in the unsaved page state;
- red border: the face still needs user attention or has no saved disposition.

The user may save a cluster review before every face has a person assigned.
Faces may instead be saved as unknown, ambiguous, or don't-track. A review is
complete only when every face has an explicit saved disposition; a partial save
remains open or requires review. Individual face assignments take precedence
over cluster membership and are retained in audit history. The cluster remains
marked mixed even after all faces have been reviewed.

New clustering runs may place faces into different clusters, but existing user
assignments and audit history remain unchanged. User-facing merge and split
operations remain deferred.

User assignments, unknown/ambiguous decisions, don't-track decisions, and
reference selections are independent of clustering runs and carry forward.
Provisional person associations are tied to the run that produced them and
become stale when a new run is accepted. A new run starts with its own review
status; prior cluster-review status is not copied automatically. If a new
cluster has exactly the same membership as a previously reviewed cluster, the
prior review may be shown as a suggestion but must not silently mark the new
cluster reviewed.

Old runs remain available for viewing and audit. Their membership data is
read-only, but an old-run page may save face-level assignments if it warns the
user that the page is based on superseded clustering results. New pages use the
current active run by default.

Singleton results remain individual clusters in the data model. The review UI
may group singleton clusters into a virtual review list by proposed person for
efficiency, but it must clearly indicate that the faces do not form one visual
cluster. Cluster-level purity, connectivity, and mixed-cluster conclusions do
not apply to a virtual singleton group. Bulk assignment in such a group
requires an explicit confirmation that the user reviewed the faces
individually. Each face still receives its own assignment event and audit
record, and partial saves leave unresolved faces flagged for review.

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
- reference recommendations and recommendation-history records
- reference-selection events and current-reference projections
- `scan_runs`
- `scan_items`
- `clustering_runs`
- provisional cluster and cluster-membership records
- cluster review and assignment-disposition records
- review accounts, collection permissions, and audit events
- `schema_migrations`
- `pipelines`

People may be created provisionally during discovery when a group of faces has
no approved reference gallery. A provisional person has no user-provided name
and exists to provide a stable candidate/association target until the user
confirms, renames, rejects, or otherwise disposes of it.

### 12.1 Invariants

- One content hash maps to one asset.
- Multiple path records may reference one asset.
- An asset may have one analysis per pipeline.
- Faces belong to analyses.
- Embeddings belong to faces and inherit pipeline provenance.
- A face has at most one current confirmed identity.
- Assignment state and reference status are tracked independently.
- People have an independent current don't-track state and corresponding
  history. Faces have an independent don't-track disposition and history.
- A face may have a person assignment and a don't-track disposition, or may be
  don't-track without a person assignment.
- A rejected suggestion is not a matching example.
- Revoked assignments are not matching examples.
- Deleting a path does not delete an asset referenced by another path.
- Asset deletion requires no remaining path references and is explicit.

Filesystem creation and modification dates belong to path records. Embedded
EXIF capture date belongs to the deduplicated asset. The selected capture date
and its source are derived from those values using the documented fallback
order.

Identity and assignment events include an event ID, face-observation ID,
nullable person ID, previous and new assignment state/person, actor type,
optional account ID, source pipeline ID, action/reason, timestamp, and an
optional superseded-event ID. System events retain the recognition evidence
snapshot (similarity, margin, support, quality, and detection confidence) and
the grouping evidence snapshot (clustering run, cluster, purity/connectivity,
and agreement). The current face-assignment projection points to the latest
effective event; the event history is immutable.

Reference-selection events record the face-observation ID, person ID, account,
timestamp, prior and new reference state, related recommendation event when
applicable, action/reason, and any superseded event. A current-reference
projection supports matching queries. Only the current effective
`reference=true` projection enters the matching gallery; unselected references
remain historical but are excluded from matching.

Current assignment, reference, recommendation, and review states are
materialized projections maintained transactionally with their immutable
events. Normal review and search queries use these indexed projections rather
than reconstructing the full event history. Maintenance tooling can rebuild
projections from history and validate that they agree with the event stream.

### 12.2 People lifecycle

The data model supports merge and split history. User-facing merge/split
operations are deferred until after basic people management and confirmation
workflows are working.

People are not physically deleted automatically. A merge preserves history;
a split creates a new person and moves selected current assignments while
retaining the audit trail.

### 12.3 Identifier format

Application-generated primary keys use UUIDv7 values in lowercase canonical
string form. UUIDv7 IDs are generated by the application, provide distributed
uniqueness, and are time-sortable for local and future multi-computer use.
SQLite stores them as `TEXT`; a future PostgreSQL backend may use its native
UUID type behind the database adapter.

UUIDv7 is used for collections, assets, paths, analyses, faces, people,
clustering runs and clusters, assignment and reference events, jobs, accounts,
sessions, permissions, and audit events. Application validation must reject
malformed IDs even where the database column is represented as `TEXT`.

Identifier formats that are not UUIDs are:

- `content_sha256`: lowercase 64-character hexadecimal SHA-256;
- `pipeline_id`: deterministic identifier derived from the complete pipeline
  configuration and checksums;
- `cluster_key`: deterministic component identifier scoped to a clustering run;
- migration versions: ordered strings such as `001_initial`.

## 13. Storage and migrations

Use one SQLite database with logical collections rather than one database per
collection. This supports global people, cross-collection search, and
cross-collection deduplication while retaining collection-specific queries.

The default macOS runtime data root is
`~/Library/Application Support/face_me_bro/`. The database is
`face_me_bro.sqlite3` under that root. Configuration, models, logs, temporary
files, exports, and backups use separate subdirectories.

The CLI must provide an option to select an alternate configuration file or
configuration location. The selected configuration may set an alternate
runtime data root, including a root on an external drive. Configuration
resolution must be reported by status and diagnostic commands so the active
database and data locations are unambiguous.

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

The application creates an automatic backup before each schema migration and
provides explicit backup and restore commands. The backup location and
retention count are configurable; the initial automatic-backup retention is
three backups. User-created or explicitly exported backups are never silently
deleted.

## 14. CLI and workflows

The MVP provides both a CLI and an authenticated web review service. Exact
command names and web routes may evolve, but the CLI must support
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
- create, list, disable, enable, and manage accounts and collection access
- reset account passwords
- purge audit events by age (system-admin only)
- export and purge derived data

The CLI is organized around command families for collections, scans and jobs,
people, faces, clusters, search, configuration, status, export/backup, and
maintenance. Exact command names are finalized during the CLI prototype.

Commands support human-readable output by default and a consistent structured
JSON output mode for scripts. Structured output is a versioned contract and
includes a schema/version field, command metadata, result data, warnings, and
structured errors. Human-readable output may evolve independently. JSON
schemas remain backward compatible within a major application version.
Identity-changing commands require explicit confirmation in non-interactive
mode, using a required confirmation flag.

Identity-modifying commands require interactive confirmation by default.
Explicit non-interactive/batch mode is supported.

### 14.1 Web review service

The web review service must support authenticated accounts, collection-level
access control, review of photos, faces, people, and clusters, and recording
which account performed each confirmation, assignment, reference selection,
or undo. It must provide an explicit permission for undoing another user's
action.

The MVP authorization roles are:

- `system-admin`: manages accounts, global settings, and all collections;
- `collection-manager`: grants access to assigned collections, manages people,
  may create accounts only with permissions below their own, and may undo
  review actions within those collections;
- `reviewer`: reviews clusters and faces, assigns people, confirms identities,
  selects references, marks faces unknown, ambiguous, or don't-track, and may
  create people;
- `viewer`: has read-only access to permitted collections.

Access is granted per collection, with viewing and modification permissions
tracked independently. Every mutating action records the account, timestamp,
affected entity, prior value, and new value. Undo creates a compensating audit
event and never deletes history. Concurrent edits use optimistic version
checks; a stale page must be reloaded before it can overwrite another user's
changes. A user cannot grant access or permissions to themselves.

For the MVP, the application manages local accounts, password hashes, and
authenticated sessions. Anonymous access is disabled. Sessions use secure,
HTTP-only, same-site cookies. Password recovery is handled by a system
administrator reset rather than email recovery. The authentication boundary
must remain replaceable so OIDC/SSO or client-certificate authentication can
be added later. A proxy certificate or network location is not sufficient
authorization by itself.

The first-run bootstrap creates a `system-admin` account named `admin` with a
temporary password of `admin`. The account must require a password change
before any other web-service operation is allowed. The replacement password
must meet the minimum eight-character letters-and-numbers rule. The service
must warn while temporary bootstrap credentials remain active. Password reset
is available to system administrators through the web service and through an
explicit CLI command. Account creation, password reset, disablement, and
re-enablement are audit events.

An account is automatically disabled after 10 failed login attempts within any
30-minute period. System administrators may disable and re-enable accounts.
Audit events are retained until manually purged by a system administrator
through the CLI or administration page, using an age-based purge criterion.

All image, thumbnail, face-crop, and metadata requests go through
authenticated application endpoints. Browsers never receive unrestricted
filesystem paths or direct file-server access. Collection permissions are
checked before returning image content, face crops, relative paths, directory
metadata, capture metadata, or person/assignment information. If a
deduplicated asset has paths in multiple collections, access through one
permitted collection may return the asset, but only paths and metadata from
permitted collections are shown.

The service prevents path traversal, symlink or junction escape, and access
through unapproved alternate paths using canonical path checks and
collection-root boundaries. Worker and database paths are never exposed as
downloadable URLs. Authorization is rechecked for every request, including
requests from old review pages. Revoked collection access takes effect for
new requests immediately.

Undo targets a specific audit event and creates a new compensating event; it
never deletes the original event. For a face/person assignment, if the current
state still equals the state created by the targeted event, undo restores the
recorded previous state. If later changes exist, the system reports a conflict
and requires explicit confirmation before replacing the current state. If no
previous state is available, undo resets the face to `unassigned` and records
that fallback.

Undoing a reference selection removes the reference status created by the
targeted event. Faces linked to that person that were processed after the
reference was added are queued for reprocessing. Existing `assigned-user`
identities are not automatically changed; provisional and system assignments
may be recomputed, and recommendations and search indexes are refreshed.
Users have undo permission by default, but an administrator may revoke it for
an account. Collection managers may undo actions within managed collections
unless their permission was revoked. Undo records the actor, target event,
conflict status, and resulting state.

### 14.2 Search state filters

By default, searches and lists exclude don't-track faces and people. An
explicit include-don't-track filter includes them. Other assignment states,
including unassigned, provisional, assigned-system, assigned-user, unknown,
ambiguous, and mixed-cluster membership, are included by default where
applicable and must have filters that allow them to be excluded. Ordinary
person/photo searches include `assigned-user` and `assigned-system` results by
default. Provisional results require an explicit provisional filter. Unknown
and ambiguous results are excluded from ordinary search results unless their
specific filters are enabled. Faces that are not confirmed may be included
when the selected search/filter set includes them, and searches provide
filters for only-confirmed and only-not-confirmed results.

Review-mode searches include provisional faces by default because they still
require user review. A provisional face may appear under its associated person
but must be visibly marked provisional. Changing it to `assigned-user` removes
it from the default unconfirmed-review result set; changing it to unknown,
ambiguous, or don't-track moves it into that corresponding review category.

Review-mode searches exclude `assigned-user` and `assigned-system` results by
default. Separate filters can include either or both of those states.

Unassigned faces appear in review-mode searches by default under an Unassigned
category. This state represents a newly detected face not yet processed, a
system assignment rejected without a replacement, or a face reset when no
previous state can be restored. An unassigned face may be assigned by the
user, become `unknown` after recognition and grouping finish without an
acceptable candidate, or be changed to `ambiguous` or don't-track. Later
processing may also change it to `provisional` or `assigned-system` when the
evidence meets those thresholds. An explicit unassigned filter can show only
these faces or exclude them.

Unknown faces appear by default in review-mode searches under an Unknown
category. An unassigned face becomes `unknown` after recognition and grouping
fail to produce an acceptable person candidate. Later processing, including a
reference change, may transition an unknown face to `provisional` or
`assigned-system` when the new evidence meets the corresponding thresholds;
it remains `unknown` when no acceptable candidate is produced. A user may
override the result by assigning the face, marking it ambiguous, or marking it
don't-track.

An `ambiguous` face is an explicit user decision that the person cannot be
identified. It appears in review-mode searches by default and has its own
filter. Automatic recognition or clustering reprocessing must not silently
change it, but new candidates may be displayed for the user to reconsider. The
user may later assign it to a person, change it to unknown, mark it
don't-track, or leave it ambiguous.

An `assigned-system` face has high recognition and grouping evidence for the
same person, but the user has not confirmed it. It is excluded from default
review-mode results and appears when the `assigned-system` filter is enabled.
Later processing may retain the state, weaken it to `provisional`, change it
to `unknown`, or suggest another person when that person has stronger evidence.
Rejecting it without assigning another person returns it to its previous state,
normally `unassigned` or `provisional`. A user assignment changes it to
`assigned-user` and protects it from automatic identity reassignment. An
assigned-system face with a person assignment remains eligible for reference
recommendation.

Faces in mixed clusters remain visible by default when their assignment state
requires review, including `unassigned`, `provisional`, `unknown`, and
`ambiguous`. Mixed-cluster membership is independently filterable. The review
UI supports both showing all faces with mixed-cluster membership and showing
only faces that still need review within a mixed cluster. Faces with
`assigned-system` or `assigned-user` state follow their normal assignment-state
filters. Mixed-cluster membership does not change an individual face's
assignment state; the cluster view displays the other assigned people and the
reason the cluster is mixed.

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
- grouping runtime
- database size
- memory usage
- review-page query latency
- incremental-update time
- worker startup and recovery
- adaptive throttling

Large-library validation uses representative datasets at approximately 10,000
images/faces, 50,000 faces, 100,000 faces, and 250,000 faces. The exact
image-to-face ratio may vary, but the face-count workload must be reported.
The evaluation compares exact search and ANN at the configured transition
ranges, using exact search as the correctness reference. Numeric performance
targets are deferred until these measurements are available. The MVP remains
functionally targeted at approximately 100,000 images; one million images is
a later scalability target.

The repository benchmark command exercises these populations with deterministic
synthetic embeddings and records scan, database, memory, query, throttling,
worker-lifecycle, and exact-versus-ANN measurements. Synthetic results validate
the harness and scaling behavior only; representative private photo libraries
remain necessary for final application-performance targets.

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

Grouping scale guidance is expressed in faces rather than images. The initial
planning boundary is exact batched neighbor search below 50,000 faces,
benchmarking exact search against ANN from 50,000 to 100,000 faces, ANN as the
recommended option from 100,000 to 250,000 faces, and ANN as the default above
250,000 faces. The exact boundary remains configurable globally or per
collection and must be validated using neighbor recall, grouping quality,
runtime, memory, and incremental-update cost.

## 17. Packaging and runtime dependencies

- Do not bundle a managed Python runtime.
- Do not require a bundled application installation for the MVP.
- System-installed Python 3.11 or a separately installed Python 3.11 is the
  default prototype/runtime prerequisite.
- Setup creates a Python virtual environment.
- Direct Python dependencies are maintained in `requirements.in`.
- Setup installs the pinned direct dependencies from
  `worker/requirements.lock` and records a complete environment-specific
  freeze in `.venv/requirements.lock.txt`.
- Development-only dependencies use a separate requirements input and lock
  file when needed.
- `worker/platforms.json` records the supported Python version, platforms, and
  execution-provider constraints.
- The project does not require Poetry, `uv`, Conda, or another managed Python
  environment.
- Production dependency updates are manual and deliberate, except for urgent
  security updates.
- Every update regenerates and reviews the lock file, reruns clean-install and
  worker smoke tests, and validates model-output compatibility and relevant
  benchmarks.
- Changes to InsightFace, ONNX Runtime, model files, preprocessing, or
  execution providers create a new pipeline ID.
- The application does not update dependencies automatically during normal
  runtime.
- The previous lock file remains available to reproduce or roll back an
  installation.
- Model files are downloaded during setup, verified by checksum, and recorded
  by pipeline ID.
- The initial setup pins InsightFace 2.0.0 and ONNX Runtime 1.30.0.
- Runtime processing works without network access.
- Docker configuration may be provided as an optional reproducible environment,
  but is not the default macOS runtime.
- Code signing and notarization are not required for the personal-use MVP or
  local development. They are required before distributing a macOS
  application externally and are release-hardening work after packaging
  stabilizes.

The implementation pins Python 3.11, InsightFace 2.0.0, and ONNX Runtime
1.30.0 for the initial CPU-provider profile.
Lock files are regenerated deliberately when dependencies change and are
reviewed as part of setup validation.

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

### 19.5 Grouping and calibration evaluation

Grouping evaluation is a separate first-class activity from recognition
evaluation. It measures grouping accuracy, mixed-cluster rate, cluster purity
and connectivity, low-signal review burden, manual review effort, runtime,
memory, and scalability. The current grouping direction and small-dataset
plan are documented in:

- `docs/protocols/evaluate_face_grouping_options.md`
- `docs/plans/evaluate_grouping-small.md`

The current prototype evidence is limited to approximately 1,060 faces and 57
labeled people from one dataset, using exact neighbor search and a face-to-face
threshold of `0.45`. It compared `k=5, 10, 20, 40`; `k=40` had the best pair
F1 on that dataset. These results are initial guidance, not final production
calibration. ANN behavior, large-library performance, and reference
recommendation quality require separate evaluation.

Calibration and evaluation must distinguish recognition accuracy, grouping
accuracy, reference recommendation quality, manual review effort, and
scalability. A user may rerun calibration and change configurable values using
configuration options.

### 19.6 Packaging tests

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

The initial production recognition defaults are similarity `0.50`, margin
`0.05`, support of `5`, face quality `0.75`, and detection confidence `0.75`
for high evidence. Medium and low evidence use the values in Section 10.2.
The no-reference recognition similarity default is `0.40`; the reference-based
recognition similarity default is `0.25` for the configured recognition
decision path. These values are configurable and should be recorded with
benchmark notes when calibration is rerun.

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

## 22. Remaining validation and prototype gates

Implementation is authorized and underway. The following validation work
remains before final performance claims and release-hardening decisions:

- final application performance numbers on representative libraries;
- large-library validation using real scan/analysis workloads;
- final provider-specific throughput and long-term code-signing/notarization
  decisions;
- optimization of reference-recommendation ranking.

The following decisions are settled initial defaults but remain configurable
and may be recalibrated through the documented evaluation workflows:

- recognition similarity, margin, support, quality, and detection thresholds;
- grouping candidate set, `k`, and face-to-face threshold;
- exact-versus-ANN search selection globally or per collection;
- reference recommendation policy and stored recommendation history.

## 23. Implementation gates

These gates defined the pre-implementation approval criteria. Implementation
has now begun; remaining unchecked gates are tracked as implementation or
validation work rather than blockers to starting the project.

The project should have:

1. A confirmed scope and data model.
2. A worker protocol definition.
3. Migration and versioning rules.
4. A model-selection and dataset-creation plan.
5. Defined privacy and packaging constraints.
6. Initial acceptance criteria for scanning, analysis, recognition, search,
   and recovery.

## Current phase

IMPLEMENTATION IN PROGRESS.

The specification remains the source of requirements and acceptance criteria.
Incomplete implementation items and validation work are tracked in the
implementation plans under `docs/plans/`.
