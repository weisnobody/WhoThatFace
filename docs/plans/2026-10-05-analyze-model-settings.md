# Analyze Model Settings Tooling

Status: Draft

## Objective

Extend `prototypes/model_settings_review` with a calibration workflow that
identifies useful similarity, margin, support-count, and quality policies before
manual candidate review.

The tool should calculate reusable face-to-person scores, sweep policy values,
separate represented from unrepresented people, generate charts, and recommend
candidate decision bins for later use by the review interface and final
application.

This remains evaluation tooling. It must not modify the production database or
automatically confirm identities.

## Relationship to the existing workflow

Current workflow:

```text
blind labels → reveal candidates → human review → analyze
```

Add an earlier calibration workflow:

```text
blind labels → score matrix → policy sweep/charts
             → choose candidate settings
             → reveal candidates → human review → final analysis
```

The calibration workflow reuses stored embeddings and labels. It should not
rerun model inference for each threshold or settings combination.

## Scope

### In scope

- Reusable evaluation-face/reference-person score matrix.
- Full-gallery candidate scores, not only top-five output.
- Represented versus unrepresented population classification.
- Similarity, margin, support, and quality policy sweeps.
- Near-miss analysis using a configurable score gap.
- Three decision-bin simulation: suggestion, review, unresolved.
- CSV/JSON machine-readable output.
- Markdown report updates.
- Histograms, curves, stacked charts, and heatmaps.
- CLI execution and report reproducibility.

### Out of scope

- Model training or fine-tuning.
- Automatic identity confirmation.
- Production Go integration.
- Replacing the human blind-review step.
- Final probability calibration.
- Automatic selection of settings without user inspection.

## Phase 1: Define calibration data structures

Add versioned structures for:

- calibration run metadata;
- reference gallery membership;
- evaluation-face truth metadata;
- full candidate score rows;
- correct-person score and rank;
- margin and near-miss values;
- decision-bin results; and
- sweep settings and metrics.

Store the calibration run settings, pipeline ID, model checksums, and source
experiment ID. Keep the existing prediction records unchanged so calibration
runs can be compared with reveal runs.

The score matrix may be stored in SQLite for moderate datasets. If the matrix
becomes large, use compressed NumPy/Parquet-style artifacts while retaining a
manifest and checksums in SQLite.

## Phase 2: Build the score matrix

Implement a calibration command that:

1. Loads reference faces grouped by person.
2. Loads evaluation faces and blind labels.
3. Excludes reference faces from evaluation queries.
4. Identifies people represented in the reference gallery.
5. Calculates the best score for every evaluation face/person pair.
6. Calculates top candidate, second candidate, margin, and rank.
7. Calculates the correct-person score even when it is outside the top five.
8. Records whether the truth person is represented.
9. Records support counts and quality values.
10. Writes a reproducible score-matrix artifact.

The command must fail clearly if blind labels are incomplete or if no reference
faces exist. It should report unrepresented people separately rather than
silently treating them as ordinary recognition failures.

## Phase 3: Implement policy simulation

Implement pure functions that classify one score-matrix row under a settings
combination:

- high-confidence suggestion;
- normal review; or
- deep review/unresolved.

The simulation must also produce comparison classifications:

- correct;
- unknown correct;
- incorrect prediction;
- known person without reference;
- high-confidence false positive; and
- near miss.

Keep policy simulation independent from the browser and database write path so
it can be unit tested over synthetic score matrices.

## Phase 4: Implement sweep command

Add a CLI command or extend `analyze` with an explicit calibration mode, for
example:

```sh
python prototypes/model_settings_review/src/review_tool.py calibrate \
  --experiment /path/to/experiment \
  --similarity-range 0.25:0.55:0.01 \
  --margin-range 0.00:0.15:0.01 \
  --support-values 1,2,3 \
  --quality-values 0.20,0.25,0.30,0.35
```

Provide a documented standard grid and allow narrower custom grids. Record the
full grid in the output so runs are reproducible.

Avoid storing a separate copy of the same large score matrix for every policy
point. Derive sweep results from the reusable matrix.

## Phase 5: Generate reports and visualizations

Generate:

- calibration JSON;
- calibration CSV for spreadsheet use;
- Markdown summary;
- similarity histograms;
- margin histograms;
- top-score and correct-score distributions;
- threshold coverage/error curves;
- stacked outcome charts;
- similarity/margin heatmaps; and
- quality/support comparison charts.

Charts should include represented and unrepresented populations separately.
Each chart must identify the experiment, pipeline, settings range, and metric
definition.

The Markdown report should identify recommended candidate operating points,
but it must clearly label them as recommendations for human review rather than
automatic production settings.

## Phase 6: Integrate with reveal/review workflow

Allow a selected calibration operating point to be passed to candidate reveal.
The reveal run must store the selected settings and remain compatible with the
existing human-review page.

The review page should continue to show candidate details and post-reveal
outcome controls. Calibration output should link representative face IDs or
photo IDs for investigating errors and near misses.

## Phase 7: Tests and validation

Add unit tests for:

- cosine score aggregation;
- full-gallery ranking;
- correct-person rank and score-gap calculation;
- represented/unrepresented classification;
- high-confidence false-positive detection;
- near-miss classification;
- three decision bins;
- threshold and margin boundary behavior; and
- incomplete-label and missing-reference validation.

Add integration fixtures covering:

- a correct top-1 match;
- a wrong top-1 match with the correct person close behind;
- a correct person outside the top five;
- a known person without reference examples;
- an unknown person correctly rejected;
- an unknown person incorrectly assigned;
- low-quality faces; and
- multiple reference support counts.

Verify that repeated calibration runs over the same experiment produce the
same score matrix and metrics.

## Acceptance criteria

The implementation is complete when a user can:

1. Complete blind labels and define reference/evaluation roles.
2. Generate a reusable full-gallery score matrix.
3. Run a similarity/margin/support/quality sweep without rerunning inference.
4. See represented and unrepresented populations separately.
5. See correct, incorrect, unknown, review, high-confidence, and near-miss
   counts.
6. Inspect histograms and coverage/error charts.
7. Select candidate suggestion/review/unresolved settings.
8. Run candidate reveal using those settings.
9. Generate a final report that preserves calibration provenance and selected
   settings.

## Risks and checks

- **Gallery leakage:** ensure reference faces never become evaluation queries.
- **Unrepresented bias:** keep people without references out of recognition
  accuracy while retaining their false-positive results.
- **Top-five truncation:** calculate the correct-person score against the full
  gallery.
- **Score interpretation:** describe similarity as a model score, not a
  probability or calibrated confidence.
- **Overfitting:** preserve an untouched confirmation subset where possible.
- **Large matrices:** measure memory and storage before choosing SQLite or an
  external artifact format.
- **Policy coupling:** keep calibration simulation pure and independent from
  browser display code.

