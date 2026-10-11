# SPEC Implementation Gap Checklist

This checklist compares `docs/SPEC.md` with the implementation on 2026-10-11.
It records requirements that are missing, incomplete, or not connected to the
production workflow. The earlier `2026-10-09-implementation-initial-missed.md`
checklist is historical and should not be treated as proof that these items
are complete.

## Priority definition

- **P0** — blocks a complete MVP workflow or violates a core data/security
  contract.
- **P1** — required MVP behavior is incomplete or only available through a
  narrow prototype/API path.
- **P2** — validation, operational hardening, or documentation needed before
  claiming the requirement is complete.

## Scanning, assets, and metadata

- [x] **P0** Implement actual Live Photo and motion-photo still extraction.
  `internal/media/scanner.go` detects adjacent `.mov`/`.mp4` files and records
  metadata, but the analysis job passes the original path directly to the
  worker. It does not identify/extract and persist the reference still,
  extraction method/version, or reliably reject unsupported paired formats.
- [ ] **P1** Recheck file identity after hashing. The scanner checks size and
  modification time before hashing, but does not verify that the file did not
  change during hashing or persist a retry outcome for that case.
- [ ] **P1** Persist canonical embedded metadata. `exif_metadata_json` is
  currently never populated, and EXIF capture-date extraction only handles a
  narrow JPEG `DateTimeOriginal` path rather than the supported format stack.
- [ ] **P1** Make changed or failed analyses explicit. The analysis processor
  only persists successful analyses; it does not create/update `queued`,
  `running`, `failed`, or `completed_with_errors` analysis records with
  categorized errors.
- [ ] **P1** Validate collection-root rules before accepting a collection.
  Overlapping roots are not rejected/reported, and collection lifecycle
  behavior does not expose a complete enabled/disabled/prune workflow.

## Worker contract and analysis integrity

- [x] **P0** Reconcile the worker protocol implementation and `SPEC.md`.
  The specification examples use `protocol_version`, `command`, `path`, and
  a nested `image` response; the implementation uses `type`, major/minor
  fields, `image_path`, and flat source-dimension fields. Choose one contract,
  update both sides, and add compatibility fixtures.
- [x] **P0** Bind runtime worker settings to the registered pipeline. The CLI
  loads model/provider/checksum metadata from SQLite, but does not pass the
  pipeline's model root, provider, detector settings, or checksums into the
  worker environment/configuration. The worker primarily reads environment
  variables and defaults.
- [ ] **P1** Validate the analyzed file against the persisted asset identity.
  The worker hashes the path at request time, but the analysis processor does
  not pass the asset's stored SHA-256 to the client or reject a result whose
  content differs from the scanned asset.
- [ ] **P1** Add complete protocol validation for finite numeric values,
  coordinate bounds, source dimensions, deterministic face ordering, and
  normalized embedding state. Current validation checks embedding length and
  basic box ordering but not all requirements in Section 6.4.
- [ ] **P1** Implement per-item failure persistence and bounded item retries.
  A worker/analysis error currently aborts the handler; the affected
  `job_item` is not reliably marked failed with its categorized error and the
  remaining assets are not guaranteed to continue.
- [ ] **P2** Add real concurrent worker/CLI and worker restart integration
  tests, including timeout, malformed output, changed-file, cancellation, and
  retry scenarios.

## Recognition, clustering, and configuration

- [x] **P0** Add configuration fields for recognition thresholds and evidence
  categories: similarity, margin, support, quality, detection, purity,
  connectivity, and agreement behavior. These are currently hard-coded in
  `recognition.DefaultThresholds()`.
- [x] **P0** Add configuration fields for clustering `k`, face-to-face
  similarity threshold, candidate scope, and exact/ANN selection. The worker
  always queues clustering with `scope: all`, and `clustering.DefaultConfig()`
  supplies fixed values.
- [ ] **P1** Make the configured grouping policy effective in the production
  worker path. CLI `cluster run` accepts settings indirectly, but the normal
  scan → analysis → recognition → clustering job chain does not expose or
  persist per-run configuration from application settings.
- [ ] **P1** Restrict recognition references to compatible pipelines and record
  the reference pipeline compatibility decision. `loadReferences` reads all
  current references without joining through compatible analysis pipelines.
- [ ] **P1** Implement targeted reference-change reprocessing. The current
  `reference-reprocess` handler reruns recognition and clustering globally,
  rather than limiting work to faces considering the affected person and
  clusters affected by those faces.
- [ ] **P1** Preserve complete evidence for reprocessing and recommendations.
  Recognition candidates are persisted, but clustering-created assignments and
  recommendation refreshes do not consistently preserve the complete current
  candidate/run snapshot needed to distinguish old and new results.
- [ ] **P1** Implement a durable recommendation decision workflow. The schema
  stores recommendation rows, but `SetReference` does not link a selection to
  a recommendation ID, and there is no explicit approval/rejection/promotion
  state and timestamp for each recommendation as required by Section 10.6.
- [ ] **P1** Add person-state mutation support for `dont_track`. The database
  has `person_state_events` and a `people.dont_track` field, but there is no
  corresponding service/CLI/web operation to set or clear a person's state.

## Cluster and face review workflow

- [x] **P0** Build the complete cluster review UI. The current cluster page
  displays faces and candidate text but instructs users to use an API; it does
  not provide the specified default-person controls, per-face selectors,
  disposition controls, save buttons, partial-save notes, or green/yellow/red
  state behavior.
- [ ] **P1** Implement the complete photo/face review UI. `/review/photos`
  currently renders photo thumbnails and a description, but does not display
  numbered face boxes, assignment state, candidate choices, unknown/
  ambiguous/false-detection/don't-track controls, or reference controls.
- [ ] **P1** Add person creation, rename, and review actions to the web flow.
  The current people page is primarily a list and the person page cannot
  create a person or rename a provisional person.
- [ ] **P1** Add reference/recommendation views to the web flow, including up
  to 20 recommendations per person, approval/rejection, current references,
  and the evidence used for each recommendation.
- [ ] **P1** Implement singleton virtual review groups and their explicit
  confirmation requirement in the web UI. The data model can represent
  singleton clusters, but no corresponding virtual review experience is
  present.
- [ ] **P1** Make cluster review authorization and historical-run behavior
  complete in the UI. Old-run warnings exist, but the page still exposes raw
  IDs and does not provide the full read-only/exception-save semantics in the
  specification.
- [ ] **P2** Apply the web display conventions consistently beyond the person
  page: percentage formatting for scores and final-eight-character display
  for visible IDs on cluster, face, audit, account, and collection pages.

## Search and authorization

- [x] **P0** Correct review-mode default filtering. The current search layer
  excludes provisional/unknown/ambiguous by default, and `/review/photos`
  does not enable provisional results by default. It also does not implement
  the specified default exclusion of assigned-system and assigned-user results
  in review mode.
- [ ] **P1** Add explicit assignment-state filters to CLI and web search,
  including assigned-system, assigned-user, unassigned, provisional,
  unknown, ambiguous, confirmed-only, and not-confirmed-only behavior.
- [ ] **P1** Scope all person/candidate choices and metadata to accessible
  collections. The person detail page loads the global people list and
  candidate names, which can reveal people outside the account's permitted
  collections.
- [ ] **P1** Complete web account and collection administration. The current
  admin pages are read-only and direct users to the CLI; the specification
  requires in-scope account authorization, collection access management, and
  collection-manager account creation with restricted permissions.
- [ ] **P1** Add web operations for account creation, password reset,
  disable/enable, collection permissions, and audit retention, with role and
  CSRF enforcement.

## CLI, jobs, export, and recovery

- [x] **P0** Connect the production worker to complete scan/analysis/
  recognition/clustering progress reporting. Recognition and clustering jobs
  are enqueued, but their per-item work is not represented in `job_items`, so
  job status cannot accurately report their progress or current item.
- [ ] **P1** Make cancellation effective for all handlers. The runner cancels
  the context, but grouping and some database loops are not context-aware and
  may continue until completion before the runner returns.
- [ ] **P1** Add worker lifecycle controls. The CLI starts a worker in the
  foreground but has no persistent worker stop/status or process-management
  operation, despite the worker lifecycle and recovery requirements.
- [ ] **P1** Complete metadata export. `export metadata` omits analyses, faces,
  embeddings, pipelines, assignment/reference/recommendation histories,
  scan/job history, accounts/permissions, audit events, and other records
  needed for a complete derived-data export.
- [ ] **P1** Make derived-data purge auditable and complete. Purge deletes
  assignment and review history as well as derived projections, without an
  export/backup confirmation record or an explicit choice between current
  projections and immutable history.
- [ ] **P1** Add restore safety checks: refuse restore while another process is
  using the database, validate the backup schema/version, use an atomic
  replacement strategy, and verify the restored database before success.
- [ ] **P2** Ensure all mutating CLI operations consistently record actor
  identity and enforce the intended system-admin/collection-manager scope;
  local CLI operations currently call stores directly without authorization
  context.

## Testing, performance, and release validation

- [ ] **P1** Add end-to-end fixtures covering duplicates, renamed paths,
  rotation, unsupported/corrupt files, missing/restored paths, unknown and
  ambiguous faces, multiple people, and reference changes through the actual
  scan-to-worker-to-review pipeline.
- [ ] **P1** Add integration tests for all role/collection combinations,
  image/path authorization, old review pages, undo conflicts, and metadata
  leakage through candidate/person lists.
- [ ] **P2** Replace synthetic-only claims with representative-library
  measurements for first scan, rescan, analysis, recognition, grouping,
  review queries, memory, throttling, and incremental updates.
- [ ] **P2** Complete exact-versus-ANN validation using neighbor recall,
  grouping quality, memory, runtime, and incremental update cost at the
  specified face populations. The current ANN path is a dependency-free
  prototype and not yet a production-quality index.
- [ ] **P2** Complete clean-install, interrupted-setup, offline-runtime,
  model-checksum, Docker, and provider-specific tests on the supported Apple
  Silicon configurations.

## Documentation and contract cleanup

- [ ] **P1** Update `docs/SPEC.md` worker examples to match the selected
  implemented protocol, or update the implementation to match the normative
  examples; currently both cannot be used as a single contract.
- [ ] **P1** Mark the implementation status of the requirements above in
  `docs/SPEC.md` and remove claims that the corresponding behavior is already
  complete.
- [ ] **P2** Document the actual production command chain, including how the
  worker receives model/provider configuration, how jobs are monitored, and
  which review features are currently CLI/API-only.

