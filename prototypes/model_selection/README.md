# Model-Selection Prototype

This is a standalone validation harness for the initial InsightFace pipeline.
It is not the production Go application or the production Python worker.

The baseline is:

- Python 3.11
- InsightFace 2.0.0
- ONNX Runtime 1.30.0
- `buffalo_l`
- Apple Silicon macOS

The prototype is intended for personal/non-commercial evaluation. The
InsightFace code and pretrained model licenses are separate; review the model
terms before distributing the application.

## Prerequisites

- macOS on Apple Silicon.
- Python 3.11 available as `python3.11`.
- Network access during setup to install packages and download the model.
- Enough free disk space for the virtual environment and model files.

Python 3.9 is intentionally rejected by `setup.sh`. The current InsightFace
package requires Python 3.10 or newer.

## Setup

From this directory:

```sh
./setup.sh
```

The script creates `.venv`, installs the pinned top-level dependencies,
downloads/loads `buffalo_l`, reports ONNX Runtime providers, hashes the local
model files, and writes the resolved environment to:

```text
.venv/requirements.lock.txt
```

The default model cache is:

```text
~/.insightface/models/buffalo_l/
```

After setup, the model is local and the smoke tests do not require network
access.

To use a specific Python 3.11 executable:

```sh
PYTHON_BIN=/path/to/python3.11 ./setup.sh
```

## Runtime check

```sh
source .venv/bin/activate
python src/check_runtime.py --load-model
```

Record the JSON output on both the M1 and M4 machines. It includes Python,
InsightFace, ONNX Runtime, machine architecture, available execution
providers, selected providers, and model-file hashes.

## Single-image smoke test

```sh
source .venv/bin/activate
python src/analyze_image.py /path/to/photo.jpg
```

The command reports image dimensions, face count, bounding boxes, detection
confidence, embedding dimension, and embedding norm. It does not print or
persist the embedding vector.

Use a small local fixture set containing rotated images, multiple faces, and
different image qualities. Do not commit private images or generated output.

## Full recursive benchmark

Run the benchmark from this directory after activating the virtual
environment:

```sh
python src/benchmark.py /path/to/photos \
  --machine-label M1 \
  --output results/M1
```

The command recursively discovers all supported image files, loads
`buffalo_l`, runs one cold full-directory pass, then three warm full-directory
passes, and writes:

```text
results/M1/
├── runtime.json   # machine, package, provider, model hashes, timings
├── dataset.json   # stable dataset manifest; paths omitted by default
├── passes.json    # raw pass-level timing and count results
├── images.jsonl   # per-image results from the first warm pass
└── summary.csv    # pass-level spreadsheet-friendly summary
```

Use `--include-paths` only when the result directory is appropriately
protected. By default, image paths are represented by stable hashes so the
reports do not disclose the photo directory layout.

The default provider policy prefers CoreML and falls back to CPU. To compare
providers explicitly:

```sh
python src/benchmark.py /path/to/photos --machine-label M1 --provider coreml
python src/benchmark.py /path/to/photos --machine-label M1 --provider cpu
```

The first inference may include CoreML compilation. Compare `cold_full_directory`
and `warm_full_directory` passes separately. Run the same command and fixture
set on the M4, changing only `--machine-label` and output directory.

The benchmark does not store embeddings. It records face counts, boxes,
detection confidence, embedding dimensions/norms, decode/inference timings,
errors, and aggregate throughput.

To store embeddings for cross-machine comparison, add
`--include-embeddings`:

```sh
python src/benchmark.py /path/to/photos \
  --machine-label M1 \
  --output results/M1-with-embeddings \
  --include-embeddings
```

This stores the complete float32 embedding for each detected face from the
first warm pass in `images.jsonl`. Embeddings are encoded as base64 over the
raw little-endian float32 bytes, so the values can be reconstructed without
JSON decimal-rounding. Each vector is keyed by `path_id` and face `index`.
The option does not imply `--include-paths`.

## M1/M4 comparison

Run the same fixture set and record:

- setup duration
- model-load time
- first-image latency
- warm-image latency
- images per minute
- faces per minute
- peak memory
- selected ONNX Runtime providers
- detection differences
- failures or warnings

The first run may include model loading and CoreML compilation. Report cold
and warm timings separately.

## Limitations

- This harness does not yet create the labeled evaluation dataset.
- It does not calibrate recognition thresholds.
- It does not implement the Go/Python production protocol.
- It does not write SQLite records.
- HEIF/HEIC decoding must be verified independently on the target setup.
