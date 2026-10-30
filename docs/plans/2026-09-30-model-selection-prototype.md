# Model-Selection Prototype

Status: Draft

## Confirmed environment

- Python 3.11.
- Apple Silicon M1 with 8 GB RAM as the initial target.
- Apple Silicon M4 available for a second comparison system.
- Personal/non-commercial use during prototype evaluation.

## Objective

Validate whether the InsightFace `buffalo_l` pipeline is an appropriate first
ML pipeline for local photo indexing on Apple Silicon.

The prototype must provide enough evidence to decide whether to:

1. Adopt `buffalo_l` as the initial pipeline.
2. Evaluate `buffalo_s` because `buffalo_l` is too expensive operationally.
3. Investigate a different pipeline because of accuracy, stability, packaging,
   or licensing problems.

This is a validation prototype, not production application functionality. It
must not modify the application database or establish the final Go/Python
worker implementation.

## Scope

### In scope

- System-installed Python 3.11 plus a virtual environment.
- Pinned Python dependencies for the prototype.
- Explicit model download and checksum recording.
- InsightFace 2.0.0 with `buffalo_l` inference.
- ONNX Runtime 1.30.0 for the initial Apple Silicon runtime check.
- Local image decoding and EXIF-orientation validation.
- Face detection and embedding generation.
- Binary embedding output for measurements, with JSON export for inspection.
- A private labeled evaluation dataset.
- Recognition threshold and quality/supporting-example evaluation.
- Startup, throughput, memory, and failure measurements.
- Optional `buffalo_s` comparison if the baseline is too expensive.

### Out of scope

- Go application integration.
- SQLite schema or migrations.
- Production worker protocol.
- Production CLI commands.
- Automatic identity assignment.
- Clustering algorithm selection.
- Model training or fine-tuning.
- Public dataset creation.
- Video or RAW support.

## Constraints and principles

- Images and labels remain local and must not be committed to source control.
- Runtime inference must work without network access after setup.
- Original images must not be modified.
- Every result must record model, model-file hash, preprocessing, dependency,
  hardware, and operating-system provenance.
- Thresholds must be selected using a calibration split and evaluated on a
  separate final split.
- False positives are more costly than returning unknown.
- A model that cannot be used under the project’s licensing requirements is
  not a valid final candidate regardless of accuracy.

## Phase 0: Confirm prerequisites

Document before running measurements:

- Apple Silicon model and available memory.
- macOS version.
- Supported Python version for the prototype.
- Virtual-environment location.
- Dependency lock/install method.
- Model download location.
- Whether the prototype runs directly on macOS, in Docker, or both.
- The exact InsightFace package and model-pack versions to test.

The direct macOS environment is the primary target. Docker is an optional
reproducibility check and is not a substitute for the target runtime test.
The M1 and M4 runs must use the same pinned prototype environment and model
files where possible.

## Phase 1: Create the private evaluation dataset

Create a dataset from a user-provided photo collection using a local-only
workflow.

### Dataset contents

Include, where available:

- Several people with multiple photographs each.
- People photographed under different lighting and at different times.
- Frontal, profile, and partially turned faces.
- Blur, occlusion, low-resolution, and difficult lighting.
- Group photographs and images containing multiple faces.
- Unknown and ambiguous faces.
- Rotated and mirrored images.

The dataset should preserve references to source images without copying more
data than necessary. Labels should use stable internal IDs rather than names
in filenames.

### Dataset splits

Create three explicit splits:

- **Development:** used to debug preprocessing and result inspection.
- **Calibration:** used to choose thresholds and policy parameters.
- **Final evaluation:** used only for the final comparison and report.

Do not allow exact duplicates or near-duplicates across splits. Where
possible, reserve complete photo sessions or time periods for evaluation so
the final set does not consist of near-identical images from the calibration
set.

Record dataset metadata without storing private images in the repository:

- dataset identifier
- creation date
- source/provenance
- consent or usage basis
- image and face counts
- person counts
- split definitions
- known limitations

## Phase 2: Build the standalone inference harness

Create a disposable prototype harness with these capabilities:

- Load one named model pipeline.
- Process one image and a directory of images.
- Apply and report EXIF orientation.
- Return deterministic face ordering.
- Record original-image dimensions and face boxes.
- Generate embeddings and face-quality inputs.
- Save an analysis manifest containing provenance.
- Save embeddings in binary form and provide JSON export for inspection.
- Continue after per-image failures and categorize those failures.
- Run with network access disabled after model setup.

The harness should support repeated runs using the same manifest so that
accuracy and performance comparisons use identical inputs.

## Phase 3: Validate image and detection behavior

Before evaluating recognition, inspect the baseline output for:

- EXIF rotations.
- Mirrored images.
- Multiple faces.
- Small faces.
- Group photographs.
- Low-quality faces.
- Corrupt and unsupported files.

Confirm that boxes map to the oriented original-image coordinate system and
that the worker does not silently omit or reorder faces.

Record:

- detection recall on labeled faces
- false detections per image
- failures by image format and cause
- behavior by face size and quality category

## Phase 4: Evaluate recognition behavior

Use confirmed labels to create same-person and different-person comparisons.
Evaluate:

- similarity distributions
- false-positive rate
- false-negative rate
- precision and recall
- unknown/rejection behavior
- top-1 and top-k candidate accuracy
- margin between the first and second candidate
- effect of one versus multiple supporting examples
- effect of face-quality filtering

Choose policy values using the calibration split. Report final results only on
the final evaluation split.

The initial policy should prefer unknown over an unsupported positive
assignment. Prototype output is a suggestion; no identity should be assigned
automatically.

## Phase 5: Measure operational behavior

Run repeated measurements on the target Apple Silicon system and record:

- setup duration
- model download size
- model-load time
- worker/inference startup time
- warm per-image latency
- faces per minute
- peak memory
- CPU utilization
- disk usage
- failure and retry behavior
- behavior after network access is disabled

Measure both a representative collection and a duplicate-heavy collection.
Do not treat benchmark throughput on unrelated hardware as a project target.

## Phase 6: Conditional `buffalo_s` comparison

Evaluate `buffalo_s` only if `buffalo_l` fails an operational requirement or
is clearly unsuitable for the intended hardware.

If evaluated, hold constant:

- dataset
- preprocessing
- evaluation code
- hardware
- dependency environment where possible
- reporting definitions

Compare accuracy and resource use using the same calibration and final
evaluation procedure. Do not select the smaller model solely from model size;
its recognition quality and unknown rejection behavior must remain acceptable.

## Required artifacts

The prototype should produce:

- setup instructions
- pinned dependency information
- model manifest and file checksums
- dataset-creation instructions
- a dataset metadata manifest
- per-run provenance manifest
- detection results
- embedding outputs or references to local outputs
- threshold-calibration report
- accuracy report
- operational benchmark report
- categorized failure log
- final recommendation

Private images, labels, embeddings, and full source paths must remain outside
source control. Reports committed to the repository must contain only safe
aggregates and reproducibility metadata.

## Decision gates

`buffalo_l` is a valid initial pipeline only if:

- setup succeeds on the target Apple Silicon environment;
- model files can be verified and used offline after setup;
- EXIF orientation and bounding-box mapping are correct;
- worker/inference behavior is stable enough for batch processing;
- recognition produces useful separation between same-person and
  different-person examples;
- the calibrated false-positive behavior is acceptable for a suggestion-based
  workflow;
- resource use is acceptable for the target collection size; and
- licensing permits the intended use.

Exact numeric thresholds for “acceptable” are intentionally left open until
the dataset and first baseline measurements exist. The final report must make
those values explicit rather than leaving them implicit.

## Risks to record

- A small private dataset may not represent the eventual photo collection.
- Thresholds may overfit the calibration set.
- Face-quality scores may be weakly correlated with recognition quality.
- Model benchmark results may not predict personal-photo performance.
- Python/native dependency installation may be less reliable than inference.
- Licensing may prevent redistribution of the selected model.
- Resource behavior may differ materially across Apple Silicon systems.

## Completion criteria

The prototype is complete when it has:

1. A reproducible setup procedure.
2. A documented private dataset-creation procedure.
3. A repeatable `buffalo_l` baseline run.
4. Detection, recognition, and unknown-rejection measurements.
5. Threshold results based on separate calibration and final evaluation data.
6. Target-machine operational measurements.
7. A licensing/status note.
8. A recommendation to adopt `buffalo_l`, evaluate `buffalo_s`, or reconsider
   the pipeline.

## Open questions

- Which Python version will be supported by the project setup?
- Which exact InsightFace package/model artifact will be pinned?
- What minimum dataset size is sufficient for the first calibration run?
- What operational limits define unacceptable setup time, memory, and
  throughput?
- Which face-quality metrics are available and useful from the selected
  pipeline?
- Is the intended distribution personal/non-commercial only, or must model
  licensing support broader redistribution?
