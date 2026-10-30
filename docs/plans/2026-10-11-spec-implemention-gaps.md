# Specification and implementation gaps

This checklist records the implementation pass against `docs/SPEC.md` and
`docs/DESIGN.md`. The implementation pass did not inspect any other file under
`docs/plans/`.

## Completed implementation items

- [x] Scan records are persisted incrementally through `Scanner.OnRecord`, and
  scan finalization marks unseen paths missing. Resume/retry re-enumerates the
  collection idempotently and enqueues analysis work.
- [x] Per-file inspection failures are persisted as `failed` scan items so one
  bad path does not discard the rest of a scan.
- [x] Worker requests have a five-minute default timeout, with a configurable
  `Client.RequestTimeout`; timeout/cancellation restarts the worker.
- [x] Worker item failures use the existing bounded job retry policy rather
  than being marked permanently failed.
- [x] Worker protocol negotiation now carries major/minor versions, rejects
  major mismatches, and negotiates the supported minor version.
- [x] Worker capabilities and pipeline expectations cover pipeline identity,
  supported commands, model checksums, modules, and resource limits.
- [x] Python worker configuration passes `allowed_modules`, reports the loaded
  pipeline identity, enforces pixel/width/height limits, orders detections
  deterministically, and rechecks file identity after inference.
- [x] Analysis persistence requires pipeline provenance and valid source image
  dimensions; embedding decoding rejects non-finite values.
- [x] Reference selection now requires an eligible assigned face/person pair
  and rejects false-detection and incorrect-assignment dispositions.
- [x] Initial reference recommendations are refreshed after recognition and
  after clustering activates a run.
- [x] Clustering receives the configured recognition thresholds instead of
  constructing default thresholds internally.
- [x] Mixed-cluster default assignment is rejected. The web review page disables
  the default-person control and explains the required per-face review.
- [x] Cluster review validates all decisions before changing state and applies
  the review event, status, assignments, and dispositions in one transaction.
- [x] `OnlyConfirmed` excludes `assigned-system` faces.
- [x] Cluster list filtering and sorting cover mixed status, review status,
  person, face-count range, candidate count, face count, person, mixed state,
  review rating, and default exclusion of `don't-track` faces.
- [x] Restore dispatch is implemented in the early startup command path and
  export now has explicit `metadata`, `assignments`, and `embeddings` modes
  with format/schema versions and pipeline provenance.
- [x] Scan status exposes total and processed item counts.
- [x] Supported non-JPEG scan paths validate dimensions for TIFF, WebP, and
  HEIF-style `ispe` metadata, and EXIF date extraction supports TIFF/WebP
  metadata plus the EXIF `DateTime` fallback tag.
- [x] Motion-photo companions require an ISO-BMFF `ftyp` marker instead of
  basename pairing alone.
- [x] Singleton clusters remain separate reviewable clusters, which is an
  allowed SPEC behavior; singleton review requires an explicit decision.

## Regression tests added or expanded

- [x] Durable scan-item persistence before scan finalization.
- [x] Worker timeout, protocol capability validation, checksum/pipeline
  contracts, cancellation, malformed output, and changed-file handling.
- [x] Python worker capability reporting, protocol hello negotiation,
  deterministic detection ordering, module configuration, width/height limits,
  file-change rejection during inference, and real Pillow/OpenCV EXIF
  orientation decoding when the project virtual environment is available.
- [x] Reference eligibility and rejected-disposition protection.
- [x] Configured-threshold plumbing and recommendation refresh paths.
- [x] Atomic cluster review, invalid-member validation, singleton rules, and
  mixed-cluster default-assignment rejection.
- [x] `OnlyConfirmed` search behavior.
- [x] Existing job retry, pause, cancellation, and resumable-item coverage.
- [x] Scan cancellation preserves records delivered before cancellation.
- [x] CLI scan retry covers persisted scan items, restored/missing paths, and
  analysis-job enqueueing.
- [x] CLI restore confirmation and all three versioned export modes.
- [x] Viewer rejection for a mutating web API.
- [x] Unauthenticated protection for every mutating web route and symlink
  escape rejection when serving photo paths.
- [x] Web permission tests cover revoked access and duplicate assets with paths
  in separately authorized collections.
- [x] Superseded cluster review API attempts are rejected as read-only before
  state mutation.
- [x] Offline model checksum creation and tamper detection.
- [x] Setup/Docker contract test verifies atomic temporary venv setup,
  cleanup/recovery hooks, offline validation, locked dependencies, and the
  worker container entrypoint.
- [x] Clean Apple Silicon setup was executed in a fresh temporary Python 3.11
  environment with the locked dependencies and model, including offline
  revalidation and interrupted-install cleanup.
- [x] Prototype tests skip optional NumPy-dependent cases cleanly when the
  dependency is not installed.

## Remaining gaps requiring additional format/integration environments

- [x] Scan/job recovery coverage now spans cancellation and pause/resume
  controls, persisted items, missing/restored paths, retry exhaustion, and
  malformed/slow output from a real subprocess worker. The CLI retry test and
  worker/job integration tests cover the transitions independently so failures
  remain diagnosable.
- [x] Add model-backed InsightFace inference tests for EXIF-orientation
  coordinates and face output using an installed `buffalo_l` model. The test
  runs when the model manifest is available and remains conditionally skipped
  in environments without model artifacts.
- [ ] Execute the Docker build/run workflow. The Dockerfile contract is tested,
  but no Docker-compatible runtime is installed on this machine. Verified
  absent: Docker, Podman, nerdctl, Lima/Colima, BuildKit, and containerd.
  Completing this item requires installing or providing a container runtime.

## Verification

The implementation pass was verified with:

```text
GOCACHE=/private/tmp/whothatface-go-cache go test ./...
GOCACHE=/private/tmp/whothatface-go-cache go vet ./...
python3 -m unittest worker.test_insightface_worker
python3 -m unittest scripts.test_setup_models
python3 -m unittest scripts.test_setup_worker
python3 -m unittest discover -s prototypes/model_settings_review/tests
python3 -m py_compile worker/insightface_worker.py scripts/setup_models.py scripts/test_setup_models.py
WTFACE_MODEL_ROOT=/private/tmp/whothatface-model-test /private/tmp/whothatface-clean-venv-20261011/bin/python -m unittest worker.test_insightface_worker scripts.test_setup_models scripts.test_setup_worker prototypes.model_settings_review.tests.test_review_tool
bash -n scripts/setup_worker.sh
git diff --check
```
