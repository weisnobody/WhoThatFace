# Identify Model Settings Evaluation Protocol

## Purpose

This protocol serves two related purposes:

1. Validate that the complete `buffalo_l` face-analysis pipeline is suitable
   for this application.
2. Identify the settings required to use the pipeline safely and effectively.

The second purpose is the primary one. The protocol does not train or modify
the `buffalo_l` model. It determines how the application should interpret
its outputs.

```text
model-selection prototype → Is buffalo_l operationally suitable?
evaluation protocol       → How should we configure and trust its results?
```

The protocol evaluates the complete application pipeline:

```text
image preprocessing
  → face detection
  → alignment
  → embedding generation
  → similarity matching
  → threshold and quality policy
  → suggestion or unknown
```

## Decisions produced by the protocol

The evaluation must identify or constrain:

- similarity threshold
- margin over the second-best person
- minimum supporting-example count
- face-quality threshold
- detection-confidence policy
- conditions for a high-confidence suggestion
- conditions for manual review
- conditions for returning unknown

These settings are application configuration, not model parameters.

## Dataset requirements

Use a private, locally stored labeled dataset separate from the performance
benchmark.

Recommended starting size:

- 15–20 known people.
- 8–15 images per person.
- At least 150 labeled face instances.
- 25–50 unknown faces.
- Deliberately confusing different-person pairs.
- Group photos and multiple-face images.
- Difficult pose, lighting, blur, occlusion, and age variation.

The dataset may begin smaller for exploratory work, but results from a small
dataset must not be treated as final threshold validation.

### Dataset labels

Each face should be labeled as one of:

- known person ID
- unknown person
- ambiguous/unusable
- no face, where applicable for detection tests

Labels should use stable internal IDs. Private images, embeddings, and
identifying metadata must not be committed to source control.

## Dataset splits

Use three explicit splits:

### Development split

Used for debugging image decoding, orientation, face ordering, and result
inspection. It may be used repeatedly during development.

### Calibration split

Used to select thresholds and policy settings. It may be used to tune the
operating point, but not to report final performance.

### Final evaluation split

Locked until all thresholds and policy settings have been selected. It is run
once for the final evaluation report.

Splits should be session-based rather than random where possible. Do not place
exact duplicates, burst photos, or near-identical images in different splits.
Known people should appear in both gallery and probe data, but different photo
sessions should be reserved for evaluation. Unknown people should not appear
in the known gallery.

## Recognition scenarios

Evaluate separately under these conditions:

- One confirmed example per person.
- Three confirmed examples per person.
- Five or more confirmed examples per person.
- High-quality faces.
- Low-quality faces.
- Difficult pose, lighting, blur, and occlusion.
- Known people under changed conditions.
- Unknown people.
- Similar-looking non-matches.

This measures the effect of the supporting-example policy and prevents a
threshold that works only for ideal frontal faces from being adopted as the
general setting.

## Metrics

Use open-set 1:N recognition metrics at the application decision level:

- Correct suggestion rate.
- False-positive suggestion rate.
- Unknown rejection rate.
- Top-1 candidate accuracy.
- Top-k candidate accuracy.
- Similarity margin between the first and second candidate.
- Coverage: percentage of queries receiving a suggestion.
- Review rate: percentage requiring manual review.
- Results by face-quality bucket.
- Results by supporting-example count.

False positives must be measured as incorrect application suggestions, not
only as pairwise embedding mismatches.

Also record detection and pipeline correctness:

- face detection recall
- false detections per image
- bounding-box consistency
- embedding dimension and normalization
- cross-machine embedding consistency
- failures by image format and cause

## Initial acceptance targets

These are starting goals for calibration and may be revised after the first
dataset is evaluated:

- Zero observed high-confidence false-positive suggestions on the final
  evaluation set.
- At least 90% correct high-confidence suggestions for medium- and
  high-quality faces with three or more supporting examples.
- At least 95% rejection of unknown faces at the high-confidence threshold.
- Low-quality or ambiguous faces may remain unknown.
- One-example profiles use a lower-confidence tier and are not held to the
  same coverage target.

Every identity assignment requires user confirmation. Even so, false-positive
suggestions should be minimized because they reduce user trust and can lead to
incorrect confirmed examples.

## Calibration procedure

Using only the calibration split, sweep combinations of:

- similarity threshold
- top-two margin
- minimum support count
- face-quality threshold
- detection-confidence policy

Select the operating point that maximizes useful suggestions while satisfying
the false-positive constraint. Classify outcomes as:

- high-confidence suggestion
- review candidate
- unknown

After selecting the settings, freeze them and run the final evaluation split
without further tuning.

## Required report

The final report must preserve:

- pipeline ID
- model and model-file checksums
- preprocessing and alignment settings
- dataset manifest and split definitions
- selected thresholds
- metric definitions
- results by quality bucket
- results by supporting-example count
- false-positive and false-negative examples
- unknown/review outcomes
- known limitations

The report must state whether the result is:

- exploratory
- suitable for prototype use
- suitable for MVP settings
- insufficient and requiring another model or dataset

## Interpretation

A poor result does not immediately mean that `buffalo_l` is unsuitable. The
failure may instead be caused by:

- incorrect EXIF orientation
- poor face detection
- insufficient gallery examples
- an unsuitable quality metric
- thresholds tuned on unrepresentative data
- an overly strict or overly permissive support policy

The protocol should first identify which part of the pipeline or policy is
responsible before replacing the model.
