# Simplified Model-Settings Review Tooling

Status: Draft

## Objective

Create local tooling that makes it practical to identify initial face-detection
and recognition settings using the simplified human-review protocol in
`docs/protocols/identify_model_settings-simplified.md`.

The tooling should process a directory of photographs, provide a blind review
of detected faces and provisional identity groups, reveal model suggestions
only after the reviewer records their own judgment, and produce a report that
helps choose thresholds and support/quality policies.

This is evaluation tooling, not the production application or its final web
interface. It must not modify the production database.

## Outcomes

The tool must make it possible to answer:

- Which faces were detected, missed, or falsely detected?
- Which detected faces belong to the same manually identified person?
- Which model suggestions are correct, incorrect, uncertain, or appropriately
  unknown?
- How do results change with similarity threshold, top-two margin, supporting
  example count, and face quality?
- Are provisional groups useful enough to support discovery-first review?

The output is an evidence-backed recommended settings file and a review report,
not a trained model.

## Scope

### In scope

- Recursive processing of supported still-image files.
- InsightFace `buffalo_l` inference using the existing prototype environment.
- Persistent local experiment data and provenance.
- Face boxes, embeddings, detection confidence, and quality-related values.
- Blind human labeling of reference and evaluation faces.
- Optional provisional face grouping for discovery review.
- Model-candidate reveal after blind labeling.
- Threshold and support-policy analysis.
- Markdown and machine-readable reports.
- Local-only browser interface.

### Out of scope

- Production Go application integration.
- Production SQLite schema or migrations.
- Automatic identity assignment without user confirmation.
- User-facing merge/split operations in the production application.
- Model training or fine-tuning.
- RAW, video, or cloud processing.
- Public dataset creation.

## Recommended first experiment

Use one private dataset containing approximately:

- 15–20 known people;
- 250–400 total photos;
- 5 varied reference photos per known person;
- 8–12 evaluation photos per known person; and
- 30–50 photos containing people outside the known-person set, if available.

The tool should also support a smaller exploratory run with at least 10 people,
100 photos, 3 reference photos per person, and 5 evaluation photos per person.
Reports from the smaller run must be labeled exploratory.

Reference and evaluation photos may be selected from one processed directory,
but the same face instance and near-duplicate/burst photos must not cross the
boundary. Reference examples are manually confirmed before they are used for
candidate generation.

## Proposed architecture

Build a standalone local service with three layers:

```text
photo directory
    ↓
inference/import command
    ↓
local experiment store
    ↓
browser review service  ←→  review database/files
    ↓
analysis and report command
```

The first implementation may use Python for the inference harness and local
web service because it reuses the existing model-selection environment. Keep
the interfaces between import, review, and analysis explicit so the tool can
later be replaced or integrated with the Go application.

### Experiment store

Use a self-contained experiment directory outside the repository. It should
contain:

- experiment manifest and protocol version;
- dataset manifest with content hashes, paths, dimensions, and capture dates
  where available;
- model and dependency provenance;
- one record per image;
- one record per detected face;
- embeddings in the existing binary representation, with optional JSON export;
- candidate results for the selected settings;
- review labels and events;
- analysis outputs and generated reports.

Keep source image paths in the manifest rather than copying images by default.
The browser must validate that a requested path still matches its recorded
content hash before displaying or processing it.

## Phase 1: Define the experiment format

Create versioned schemas for:

- experiment metadata;
- dataset/image records;
- face detections;
- person labels;
- reference/evaluation membership;
- blind review decisions;
- candidate predictions;
- post-reveal review decisions;
- analysis results.

Every record should include stable IDs rather than relying on array positions or
filesystem paths. Record the protocol version, model pipeline, model-file
checksums, settings, timestamp, and source image hash.

Define review decisions separately from model predictions. In particular, do
not overwrite a reviewer label when a model candidate is revealed.

## Phase 2: Import and inference command

Add a command that accepts an input directory and creates an experiment:

1. Recursively enumerate supported still images.
2. Calculate content hashes and preserve relative paths.
3. Record image dimensions, capture date when available, and decode failures.
4. Run face detection and embedding generation.
5. Store deterministic face ordering and original-image coordinates.
6. Store detection confidence, embedding metadata, and quality inputs.
7. Record per-image success/failure without aborting the full import.
8. Save all model, runtime, and dataset provenance.

The command must support resume and a verify-only mode. Re-running it against
unchanged content should not create duplicate image or face records.

## Phase 3: Blind review interface

Implement a local browser interface with:

- a dataset/photo queue;
- a photo viewer with numbered face boxes;
- a face list for the selected photo;
- a provisional face-group view;
- a people panel for known-person labels;
- controls to create an internal person label;
- controls to assign a face to a person;
- controls to mark a face unknown, ambiguous/unusable, or false detection;
- a way to record a missed face;
- progress and incomplete-review indicators.

The blind review must hide model identity candidates, similarity scores, support
counts, and model-derived person names. Detection boxes and neutral face
numbers may be shown because detection review is part of the protocol.

The reviewer should be able to label reference faces first, then label
evaluation faces independently. The interface must visibly distinguish the two
sets and prevent an evaluation face from becoming a reference example before
the blind evaluation decision is recorded.

## Phase 4: Reference examples and candidate generation

After reference labels are complete:

1. Build a reference gallery from confirmed reference faces.
2. Record which examples support each person.
3. Generate candidates for evaluation faces using cosine similarity.
4. Calculate top-k candidates, top-two margin, support count, and face-quality
   values.
5. Apply configurable detection, quality, similarity, margin, and support
   policies.
6. Classify each result as high-confidence suggestion, review candidate, or
   unknown.

Candidate generation must be reproducible. Store the settings used for every
run and allow a new settings run without changing the original blind labels.

## Phase 5: Reveal and post-reveal review

Add a second interface state that reveals candidates only after the relevant
blind label exists. For each face, show:

- reviewer-assigned person or unknown status;
- model's top candidate and top-k candidates;
- similarity scores and top-two margin;
- supporting-example count;
- face-quality and detection-confidence values;
- model decision category;
- controls for correct, incorrect, appropriately unknown, too cautious, or
  too confident.

The reviewer must be able to flag cases where the correct person appears in
the candidate list but was not selected, and cases where a provisional group
contains multiple people.

## Phase 6: Analysis and settings recommendation

Implement an analysis command that reads the experiment store and evaluates a
grid of policy settings without re-running face detection or embedding
generation.

Analyze at minimum:

- detection recall and false detections per image;
- top-1 and top-k identity accuracy;
- false-positive suggestion rate;
- unknown rejection rate;
- coverage and manual-review rate;
- similarity and margin distributions;
- results by support count;
- results by face-quality bucket;
- results for known and unknown people separately;
- provisional-group purity, fragmentation, and mixed-person groups.

The analysis should favor false-positive avoidance over coverage. It should
identify candidate settings that meet the following initial policy goals:

- no observed high-confidence false-positive suggestions in the reviewed set;
- useful high-confidence coverage for medium- and high-quality faces;
- strong rejection of unknown people;
- difficult faces routed to review rather than forced into an identity.

These are starting goals, not permanent acceptance criteria.

Generate:

- a human-readable Markdown report;
- a machine-readable JSON report;
- a settings recommendation containing thresholds, support requirements,
  quality policy, and known limitations;
- links or stable IDs for representative successes and failures.

## Phase 7: Validation and repeatability

Test the tooling with fixtures containing:

- multiple faces per image;
- rotated images;
- duplicate content at different paths;
- corrupt and unsupported files;
- no-face images;
- unknown people;
- ambiguous and low-quality faces;
- repeated imports and resumed imports;
- incomplete blind reviews;
- settings reruns after review data is complete.

Confirm that:

- model results remain hidden during blind review;
- reference leakage is prevented or clearly reported;
- candidate runs are reproducible;
- changing thresholds never changes reviewer labels;
- missing source files are reported safely;
- private images and embeddings stay outside source control;
- reports contain enough provenance to compare experiments.

## Acceptance criteria

The plan is complete when a user can:

1. Point the import command at a private photo directory.
2. Open the local review interface and label reference faces without seeing
   model identities.
3. Label evaluation faces without seeing model identities.
4. Generate and reveal model candidates.
5. Record corrections and uncertainty judgments.
6. Run analysis across multiple threshold/support/quality settings.
7. Receive Markdown and JSON reports with a recommended operating point and
   representative errors.
8. Repeat the experiment on another dataset without changing the code or
   mixing the datasets.

## Open decisions before implementation

- Whether the first review service should use a minimal Python web framework
  or a static frontend with a local API.
- Whether provisional grouping is implemented in the first tooling milestone
  or represented initially as a manually curated gallery.
- The exact image-display mechanism for local files and browser security.
- The quality metric and quality buckets exposed in the first version.
- Whether evaluation-set membership is selected by the tool or imported from a
  user-maintained manifest.
- The minimum required review completion percentage before analysis is allowed.
- Whether the first report should include interactive plots or only Markdown
  tables and example links.

## Risks and prototype checks

- **Blind-review leakage:** verify that candidate data is not present in the
  API responses or page source before reveal.
- **Reference contamination:** reject or flag evaluation faces used as gallery
  examples; detect exact and near-duplicate image leakage where practical.
- **Poor grouping quality:** test several clustering settings and inspect
  mixed-person and fragmented groups before making grouping central to review.
- **Large-image browser performance:** test representative image dimensions
  and avoid copying or decoding every image at page load.
- **Path and privacy issues:** test missing paths, external-volume paths,
  Unicode names, and experiment-directory permissions.
- **Settings overfitting:** reserve a small untouched subset for confirmation
  after settings are chosen.
- **Confusing evidence with labels:** keep reviewer ground truth, model
  suggestions, and final user corrections as separate records.

