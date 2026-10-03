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

## Review

Start the local service:

```sh
python prototypes/model_settings_review/src/review_tool.py serve \
  --experiment /path/to/experiments/sample
```

Open <http://127.0.0.1:8765/>. The interface shows photos and numbered face
boxes but hides candidates and scores until the blind labels are complete.

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

## Limitations

- The first grouping view uses a simple deterministic embedding threshold; it
  is provisional and not a production clustering algorithm.
- Capture dates are recorded when available through common EXIF tags, but the
  prototype does not require them.
- Face-quality is currently a transparent heuristic based on detection score,
  face size, and image dimensions. It is intended for calibration, not as a
  final quality model.
- The browser service is intentionally local-only and has no authentication.
