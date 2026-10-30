# Analyze Model Settings Protocol

## Purpose

This protocol identifies useful similarity, margin, support-count, and
face-quality settings before relying on the candidate-review workflow.

It uses the manually reviewed labels and stored embeddings to evaluate many
policy settings without rerunning face detection or embedding generation.

The protocol distinguishes two populations:

- **Represented people:** people with at least one confirmed reference face.
  These can be evaluated for recognition accuracy.
- **Unrepresented people:** people labeled by the reviewer but with no
  reference face in the gallery. These cannot be fairly evaluated for correct
  identification. They are evaluated for inappropriate assignment to another
  represented person.

Unrepresented people must not be mixed into the normal recognition-accuracy
denominator.

## Required inputs

The experiment must contain:

- stored face embeddings;
- face-quality values;
- blind labels for evaluation faces;
- reference labels for the gallery;
- person assignments for known faces;
- model and pipeline provenance; and
- a record of the reference/evaluation roles.

Every evaluation face must have a reviewer label before calibration begins.
Reference faces must not also be used as evaluation queries.

## Score matrix

The calibration tool should calculate scores once and retain a score matrix for
reuse across policy sweeps.

For each evaluation face, calculate against every person with reference
examples:

- best similarity score for each person;
- top candidate and score;
- second candidate and score;
- top-two margin;
- correct person's score, when represented;
- correct person's rank;
- difference between the top score and correct-person score;
- number of reference examples supporting each person; and
- face quality and detection confidence.

The correct-person score must be calculated even when that person is not in
the top five candidates. Storing only the top five candidates is insufficient
for calibration.

The initial person score is the strongest similarity to that person's
reference examples, matching the current recognition implementation. The
calibration output must record this aggregation rule.

## Population classifications

### Represented people

For evaluation faces whose true person has reference examples, classify each
policy result as:

- correct top-1 identification;
- incorrect top-1 identification;
- correct person in top-k;
- correct person absent from top-k;
- correct person within a specified score gap of the winner;
- rejected as unknown;
- routed to review because of margin;
- routed to review because of quality; or
- routed to review because of insufficient support.

### Unrepresented people

For evaluation faces whose true person has no reference examples, classify each
result as:

- correctly rejected as unknown/unrepresented;
- incorrectly assigned to a represented person; or
- high-confidence false assignment.

These results measure false-positive risk and gallery coverage, not recognition
accuracy for the unrepresented person.

## Policy sweep

Run a configurable grid without rerunning inference. The initial exploratory
grid should include:

- similarity threshold from `0.25` through `0.55`, in `.01` increments;
- margin threshold from `0.00` through `0.15`;
- minimum support counts of `1`, `2`, and `3`, where available; and
- several quality thresholds, including the current default and quality
  percentiles.

The range may be expanded after inspecting the observed score distributions.

For each settings combination, calculate:

- top-1 accuracy;
- top-k accuracy;
- correct rejection of unrepresented/unknown faces;
- incorrect assignment rate;
- high-confidence false-positive rate;
- coverage, meaning the percentage receiving a suggestion;
- review rate;
- unresolved/unknown rate;
- results by quality bucket;
- results by support count; and
- results for represented and unrepresented populations separately.

Also calculate near-miss counts. A near miss occurs when the correct person's
score is below the winner but within a configured gap `X`:

```text
top_score - correct_person_score <= X
```

Near misses indicate ambiguity that a margin policy may correctly route to
review rather than treating as a confident error.

## Suggested decision bins

The analysis should support three application decision bins:

### High-confidence suggestion

The best candidate satisfies the high similarity threshold, margin threshold,
quality threshold, and minimum support requirement.

### Normal review

There is a plausible candidate, but one or more policy conditions are weaker,
such as a small margin, moderate similarity, limited support, or borderline
quality.

### Deep review or unresolved

There is no sufficiently supported candidate, the face is poor quality, the
candidate scores are too close, or the face appears unknown/unrepresented.

These bins are decision-policy labels, not calibrated probabilities.

## Required visualizations

The calibration output should include, at minimum:

- similarity histograms for correct and incorrect top candidates;
- similarity histograms for represented and unrepresented people;
- top-score distributions;
- correct-person score distributions;
- margin histograms for correct and incorrect results;
- correct-person rank distribution;
- stacked outcome counts by similarity threshold;
- coverage, accuracy, and false-positive curves;
- a similarity-versus-margin heatmap;
- quality-bucket performance; and
- support-count performance.

The most important summary should show, for each threshold or settings point:

- coverage;
- correct rate;
- false-positive rate; and
- review rate.

## Recommended interpretation

Prioritize settings that eliminate high-confidence false positives, then
increase useful coverage without materially reducing correct rejection of
unknown and unrepresented people.

Do not tune settings from a single example. Investigate representative false
positives, false negatives, and near misses after selecting candidate operating
points.

The final selected settings should be run through the separate candidate
reveal and human-review workflow before being considered for MVP use.

