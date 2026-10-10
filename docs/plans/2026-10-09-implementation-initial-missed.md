# Initial Implementation — Missed Requirements Checklist

This checklist records the requirements from `docs/SPEC.md` that were not
complete in the current implementation audit. It is intended to supplement
`docs/plans/2026-10-09-implementation-initial.md`.

## Scanning and metadata

- [x] Extract EXIF capture dates and persist the selected date source.
- [ ] Detect and process iOS Live Photos and Android-style motion photos using
      their reference still component.
- [x] Persist scan runs and per-path scan items.
- [ ] Implement resumable scan progress.
- [ ] Add scan pause, resume, cancel, and retry operations.
- [ ] Handle symlinks and junctions explicitly and safely.
- [x] Persist categorized unsupported, corrupt, and unstable path outcomes.

## Worker contract and reliability

- [ ] Validate worker capabilities, model checksums, preprocessing settings,
      and pipeline compatibility at startup.
- [x] Implement explicit protocol minor-version negotiation.
- [x] Validate expected file identity before accepting analysis results.
- [x] Restart workers after timeout, malformed output, or process failure.
- [ ] Persist bounded retry attempts and permanent failures.
- [x] Implement cancellation through worker termination and restart.
- [ ] Implement EXIF orientation and coordinate mapping in the inference path.
- [ ] Support pipeline-specific model configuration and checksum validation.

## Recognition and clustering integration

- [ ] Create and maintain provisional person records for discovery-first
      clusters where appropriate.
- [ ] Calculate recognition/grouping agreement for candidate people.
- [ ] Use combined evidence to derive assignment states.
- [ ] Persist candidate lists and complete evidence snapshots.
- [ ] Wire reference changes to recognition, affected clustering, and
      recommendation reprocessing.
- [ ] Preserve and recompute system/provisional assignments without changing
      user-confirmed assignments.
- [ ] Validate purity and connectivity against the grouping benchmark
      definitions.

## Cluster review workflow

- [ ] Add cluster detail pages with face images and candidate evidence.
- [ ] Implement cluster default-person assignment.
- [ ] Implement green, yellow, and red face-state borders.
- [ ] Support partial saves and per-face dispositions.
- [ ] Support mixed-cluster review and explicit needs-review states.
- [ ] Support unknown, ambiguous, false-detection, and don't-track actions.
- [ ] Support singleton virtual review groups with explicit confirmation.
- [ ] Add superseded-run warnings and historical-run review behavior.
- [ ] Add optimistic concurrency/version checks for review mutations.
- [ ] Record all cluster review actions in the audit history.

## Web service, authorization, and audit

- [ ] Integrate audit recording into every mutating review and account action.
- [ ] Implement targeted undo with conflict detection and compensating events.
- [ ] Enforce system-admin, collection-manager, reviewer, and viewer roles.
- [ ] Scope every people, cluster, photo, metadata, and image query to
      authorized collections.
- [ ] Complete the authenticated review UI for photos, faces, people, and
      clusters.
- [ ] Add collection management and account administration pages as required.
- [ ] Ensure image, face-crop, metadata, and path access never bypasses the
      authenticated service.

## CLI and operational controls

- [ ] Add collection remove and lifecycle commands.
- [ ] Add scan status, pause, resume, cancel, and retry commands.
- [ ] Add job pause, resume, cancel, retry, and detailed-status commands.
- [ ] Add face and cluster inspection/review commands.
- [ ] Add configuration/model-settings commands.
- [ ] Add export, derived-data purge, database backup, and restore commands.
- [ ] Make structured JSON output consistent across all command families.
- [ ] Require explicit confirmation for all identity-changing non-interactive
      commands.

## Background processing and resource policy

- [ ] Implement automatic adaptive, full-speed, reduced, and paused modes.
- [ ] Use system activity, load, thermal pressure, power state, and application
      activity as throttling signals where available.
- [ ] Provide manual throttling controls when activity detection is not
      available or reliable.
- [ ] Make paused and cancelling job states effective in the runner.
- [ ] Track per-item progress, retries, partial completion, and recovery.
- [ ] Ensure failures remain inspectable and retryable without restarting all
      work.

## Packaging and setup

- [ ] Add setup automation for the Python virtual environment.
- [ ] Generate and maintain a complete pinned dependency lock file with
      supported platform/provider metadata.
- [ ] Download and checksum-verify model files during setup.
- [ ] Add interrupted-setup recovery and clean-install validation.
- [ ] Validate offline runtime after setup.
- [ ] Provide the optional Docker configuration.
- [ ] Document installation, model setup, configuration, and worker startup.

## Performance and evaluation

- [ ] Add benchmarks for first scan and unchanged rescan.
- [ ] Add duplicate-heavy collection benchmarks.
- [ ] Measure analysis, recognition, grouping, database-size, and memory
      performance.
- [ ] Measure review-page query latency and incremental-update time.
- [ ] Measure worker startup, timeout, restart, and recovery behavior.
- [ ] Benchmark adaptive throttling.
- [ ] Implement exact-versus-ANN comparison tooling.
- [ ] Add configurable exact/ANN selection globally and per collection.
- [ ] Run the specified 10,000-, 50,000-, 100,000-, and 250,000-face tests.

## Documentation consistency

- [x] Update `docs/SPEC.md` to reflect that implementation is authorized and
      underway rather than saying `DESIGN ONLY`.
- [x] Remove or revise the obsolete implementation-gate language.
- [x] Verify the worker response example does not contain the previously
      reported duplicated `image` object.
- [ ] Mark completed, partial, and deferred requirements explicitly as the
      implementation progresses.
