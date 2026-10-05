# Simplified Model-Settings Review Tool

This prototype implements the workflow in
`docs/protocols/identify_model_settings-simplified.md`:

1. Import a recursive directory and run local `buffalo_l` inference.
2. Review detected faces without seeing identity predictions.
3. Mark reference and evaluation faces with person labels or unknown states.
4. Reveal recognition candidates after the blind labels are saved.
5. Generate a Markdown and JSON settings report.

It is evaluation tooling only. It does not use or modify the production
database.

## Setup

Run from the repository root with the model-selection virtual environment:

```sh
source prototypes/model_selection/.venv/bin/activate
```

The tool uses the dependencies already installed for the model-selection
prototype. No network access is required after `buffalo_l` has been loaded.

## Import

```sh
python prototypes/model_settings_review/src/review_tool.py import \
  /path/to/photos \
  --experiment /path/to/experiments/sample
```

Optional flags:

```text
--provider auto|coreml|cpu
--det-size 640
--omit-paths
```

The experiment directory contains `experiment.sqlite3` and
`experiment.json`. The database stores embeddings as binary float32 blobs.
Every discovered path is retained, while duplicate content is inferred once.
Source paths are stored by default because the browser must load local images;
use `--omit-paths` when creating an analysis-only experiment.

The importer stores the file's filesystem creation date in
`images.capture_date` as the dataset date fallback. For an existing
experiment, populate empty dates without rerunning inference:

```sh
python prototypes/model_settings_review/src/review_tool.py backfill-dates \
  --experiment /path/to/experiments/sample
```

## Review

Start the local service:

```sh
python prototypes/model_settings_review/src/review_tool.py serve \
  --experiment /path/to/experiments/sample
```

Open <http://127.0.0.1:8765/>. The interface shows photos and numbered face
boxes but hides candidates and scores until the blind labels are complete.
The photo list can be sorted by file name or creation date and filtered by a
substring of the relative path.

For each face, select a person or create one, choose `reference` or
`evaluation`, and save. Use `unknown`, `ambiguous`, or `false_detection` when
appropriate. Reference faces are used to build the gallery; evaluation faces
are not eligible to become gallery examples.

When labeling is complete, click **Reveal candidates**. The reveal is recorded
in the experiment and predictions are persisted. The settings controls then
allow a new threshold/margin/support/quality analysis without rerunning the
model.

## Analyze from the command line

```sh
python prototypes/model_settings_review/src/review_tool.py analyze \
  --experiment /path/to/experiments/sample
```

Use `--sweep` to evaluate the standard grid of similarity, margin, support,
and quality settings in one run:

```sh
python prototypes/model_settings_review/src/review_tool.py analyze \
  --experiment /path/to/experiments/sample --sweep
```

Reports are written to the experiment directory as
`report-YYYYMMDD_HHMMSS.md` and `.json`.

## Result types and follow-up actions

The report and the Photos result filter use the following result types. Some
types describe the ground-truth comparison, while others describe the model's
decision policy. They are therefore not mutually exclusive: one face can be,
for example, both `unknown_prediction` and `known_person_without_reference`.
The report counts faces; the Photos filter shows photos containing at least one
matching face.

### Detection and review results

#### `missed_faces_recorded`

The reviewer recorded that a photo contains a face that the detector did not
find. This is recorded at the photo level because there is no detected face
record to classify.

Action: inspect the image and confirm that it is a real face rather than an
irrelevant face-like region. Keep examples of missed faces for later detector
threshold or model investigation. It is not fixed by assigning a person in the
current tool.

#### `review_required`

The matcher found a leading candidate, but the result did not satisfy the
high-confidence policy. This can be caused by insufficient similarity, a small
top-two margin, too few supporting examples, or low face quality.

Action: review the face manually. Confirm a correct assignment, correct it,
or leave it unknown. Repeated patterns may justify changing thresholds,
support requirements, or the quality policy.

### Recognition comparison results

#### `correct`

The evaluation face was labeled as a known person who has at least one
reference example, and the model selected that same person.

Action: none. Preserve representative examples as evidence that the current
settings work in that condition.

#### `unknown_correct`

The evaluation face was labeled unknown, ambiguous, or a false detection, and
the model did not assign a person.

Action: normally none. For an ambiguous or false detection, separately decide
whether the detector or the review label needs attention.

#### `incorrect_prediction`

The evaluation face was not correctly handled, excluding the special case of a
known person who has no reference examples. This includes assigning the wrong
known person, failing to identify a known person who does have reference
examples, or assigning any person to an unknown/non-person face.

Action: inspect the candidate list and correct or reject the assignment. Do
not confirm an incorrect person or allow that face to become a reference
example. Look for repeated causes before changing settings.

#### `known_person_without_reference`

The reviewer identified the face as a person, but that person has no face in
the reference gallery. The matcher cannot be fairly evaluated for that person
because it has no confirmed example to compare against.

Action: for a recognition experiment, add suitable reference photos for that
person or exclude the person from the recognition accuracy denominator. In the
normal application workflow, confirm a few good examples so future photos can
be matched. This result is a dataset/gallery limitation, not automatically a
model error.

### Model decision categories

#### `unknown_prediction`

The model did not produce a person assignment because the similarity,
margin, support-count, or quality requirements were not met. It may still have
candidate people internally.

Action: compare it with the blind label. If it is a known person with adequate
reference examples, inspect whether the policy is too strict or the references
are poor. If it is genuinely unknown, this is usually the desired behavior.

#### `high_confidence`

The best candidate passed all configured high-confidence requirements:
similarity threshold, top-two margin, minimum support count, and face-quality
threshold.

Action: no action is implied by this category alone. Check whether the
high-confidence result agrees with the blind label. High-confidence results
should be especially trustworthy because mistakes in this category are costly.

#### `high_confidence_false_positive`

The model made a high-confidence person assignment, but the assignment was
not correct or the face was labeled unknown, ambiguous, or a false detection.

Action: treat this as the most urgent recognition error. Reject or correct the
assignment, ensure the face does not become a reference example, and preserve
the case for threshold and gallery analysis. Consider raising the similarity
or margin threshold, increasing the support requirement, or raising the
quality threshold only after checking that the case is representative.

### Post-reveal reviewer results

These categories are recorded when the reviewer evaluates a revealed result.

#### `incorrect_review`

The reviewer explicitly marked the revealed model result as incorrect.

Action: correct the assignment or leave the face unknown, and do not use the
bad result as a reference example. Compare it with the derived
`incorrect_prediction` classification.

#### `too_cautious`

The reviewer judged that the model should have made a stronger or more direct
person suggestion.

Action: inspect the reference gallery and consider whether the similarity,
margin, support, or quality policy is too strict. Do not lower thresholds
based on isolated cases.

#### `too_confident`

The reviewer judged that the model's confidence was too high for the evidence
available, even if the suggested person may have been plausible.

Action: treat it as a warning about user trust. Review the gallery and consider
stricter thresholds, larger margins, more supporting examples, or a higher
quality requirement.

## Recommended review order

1. Resolve `missed_faces_recorded` and obvious false detections.
2. Add reference examples for `known_person_without_reference` faces before
   judging recognition accuracy for those people.
3. Review `high_confidence_false_positive` cases first.
4. Correct `incorrect_prediction` and `incorrect_review` assignments.
5. Manually resolve `review_required` and important `unknown_prediction`
   cases.
6. Use aggregate patterns from the report to decide whether settings should
   change; do not retune from one isolated face.

## Settings and review workflow

The header controls determine how a candidate becomes a high-confidence
suggestion. They do not change the InsightFace model or the stored embeddings.
They change the policy used to interpret the embeddings.

### Similarity threshold

The similarity threshold is the minimum cosine similarity between an
evaluation face and its best supporting example. The initial default is `0.45`.

- Increase it when different people are being confused or unknown faces are
  receiving suggestions.
- Decrease it when known people with good reference examples are frequently
  returned as unknown.

Increasing this value is stricter and usually reduces false positives at the
cost of more unknown/review results. Decreasing it increases coverage but can
reduce safety.

### Margin threshold

The margin is the difference between the best candidate's similarity and the
second-best candidate's similarity. The initial default is `0.05`.

- Increase it when the top two people are often close and the selected result
  is not clearly better than the alternative.
- Decrease it only when the correct person consistently wins by a small but
  reliable margin.

A higher margin requires a clearer winner and routes more borderline faces to
review or unknown.

### Minimum supporting examples

This is the minimum number of confirmed reference examples available for the
selected person. The initial default is `1`.

- Use `1` while bootstrapping or when many people have only one reference.
- Use `2` or `3` when the dataset has enough varied reference photos and false
  positives are more important than coverage.

More support improves confidence in a person's gallery but also means that
newly created people, or people with only one good photo, will remain in
review until more examples are confirmed. The examples should be independent
photos rather than near-duplicate bursts.

### Quality threshold

The quality threshold is the minimum face-quality score. The prototype score
combines detection confidence and a face-size heuristic. The initial default
is `0.25`.

- Increase it when tiny, blurry, heavily occluded, or poorly detected faces are
  producing unreliable suggestions.
- Decrease it only when useful faces are being discarded and manual review
  confirms that the lower-quality images are still identifiable.

Quality filtering should normally route difficult faces to review rather than
force an identity. It is a prototype heuristic, not a calibrated universal
measure.

### Reveal candidates button

The button currently labeled **Reveal candidates** (also referred to as the
candidate-review step) performs the recognition pass after blind labeling:

1. It verifies that every detected face has a kind and that reference faces
   exist.
2. It builds a gallery using only faces labeled as `Reference` and assigned to
   a person.
3. It evaluates faces labeled `Evaluation`.
4. It stores the top candidates, similarity, margin, support count, quality,
   and decision category in the experiment database.
5. It exposes the candidate data in the face review panel.

Reference faces are not evaluated as queries. Faces for people without any
reference examples are reported as `known_person_without_reference` rather
than being treated as a fair recognition test.

Changing a setting and revealing candidates again replaces the stored
predictions with results from the new settings. Reviewer labels remain
separate and are not replaced.

### Analyze button and command

The web **Analyze** button summarizes the currently stored predictions and
writes a Markdown and JSON report into the experiment directory. It reports
the settings from the most recent candidate-review run; the stored prediction
results must first have been generated by that step.

For a specific settings run from the command line:

```sh
python prototypes/model_settings_review/src/review_tool.py analyze \
  --experiment /path/to/experiments/sample \
  --similarity-threshold 0.50 \
  --margin-threshold 0.08 \
  --min-support 2 \
  --quality-threshold 0.30
```

Use `--sweep` to run the standard grid of settings without rerunning face
detection or embedding generation. This is the preferred way to compare
several operating points after the blind labels and reference gallery are
complete.

### Calibration command

To analyze settings before revealing candidates, build a reusable full-gallery
score matrix and run a policy sweep:

```sh
python prototypes/model_settings_review/src/review_tool.py calibrate \
  --experiment /path/to/experiments/sample
```

The default sweep covers similarity thresholds from `0.25` through `0.55`,
margin thresholds from `0.00` through `0.15`, support counts `1,2,3`, and
quality thresholds `0.20,0.25,0.30,0.35`. Narrow or customize the sweep with:

```sh
python prototypes/model_settings_review/src/review_tool.py calibrate \
  --experiment /path/to/experiments/sample \
  --similarity-range 0.25:0.55:0.01 \
  --margin-range 0.00:0.15:0.01 \
  --support-values 1,2,3 \
  --quality-values 0.20,0.25,0.30,0.35 \
  --near-miss-gap 0.05
```

Each calibration run creates a timestamped directory containing:

- `score_matrix.jsonl`: full-gallery scores and correct-person ranks;
- `calibration.json`: all sweep settings and metrics;
- `report.md`: recommended candidate operating point and interpretation; and
- `charts.html`: a three-column visual comparison of all faces, faces whose
  assigned person has reference faces, and faces whose assigned person does
  not have reference faces; and
- `charts/{all,reference,non_reference}/`: similarity, margin,
  threshold-tradeoff, heatmap, and outcome-mix SVG charts for each population.
  The similarity and margin charts include both full-range and focused views
  (`0.20–1.00` and `0.10–1.00`, respectively); the focused views note that
  lower scores were omitted.

Calibration requires complete blind labels and at least one reference face.
It does not rerun face detection or embedding generation. It separates people
with no reference examples from normal recognition accuracy and identifies
near misses where the correct person is close to the winning candidate.

### Interpreting the generated report

The report contains:

- the settings used;
- evaluation-face and missed-face counts;
- correct and unknown-correct results;
- incorrect predictions;
- known people without reference examples;
- review-required and unknown predictions;
- high-confidence results and high-confidence false positives;
- post-review outcome counts when the reviewer has recorded them; and
- results grouped into low, medium, and high quality buckets.

Report counts are face-level. A photo can contain several counted faces, and a
photo can match more than one result filter. The web Photos result filter is
therefore a navigation aid, not a replacement for the face-level report.

Use the report to adjust one policy at a time. Prioritize eliminating
high-confidence false positives, then reduce unnecessary review/unknown
results while preserving correct rejection of genuinely unknown people.

## Limitations

- The first grouping view uses a simple deterministic embedding threshold; it
  is provisional and not a production clustering algorithm.
- Capture dates are recorded when available through common EXIF tags, but the
  prototype does not require them.
- Face-quality is currently a transparent heuristic based on detection score,
  face size, and image dimensions. It is intended for calibration, not as a
  final quality model.
- The browser service is intentionally local-only and has no authentication.


## Save Model Review ##

Use:

Correct — model selected the correct person.
Incorrect — model selected the wrong person or failed to include the correct person in its candidates.
Too cautious — the correct person was a candidate, but the model did not make a strong enough suggestion.
Too confident — the model made a strong suggestion that was not sufficiently supported.
Appropriately unknown — the face is genuinely unknown and the model left it unresolved.
