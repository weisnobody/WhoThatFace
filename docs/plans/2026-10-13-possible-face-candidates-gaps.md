# Possible Face Candidates Gap-Closure Plan

## Objective

Close the implementation gaps identified by reviewing
[`docs/possible_face_candidates.md`](../possible_face_candidates.md), the
original implementation plan, and the current code. The feature must preserve
historical candidate evidence while remaining review-only: it must not mutate
assignments or clusters, and it must not break face reprocessing or deletion.

## Priority order

1. Correctness and data-integrity blockers.
2. Scheduling, retry, and stale-result semantics.
3. Review and audit behavior.
4. Tests, fixture validation, and rollout evidence.

## Phase 1: Correctness and data model

- [x] Persist target eligibility records or extend the run model so every
  selected target face records its eligibility reason, including faces with no
  retained candidates.
  - Record the reason or reasons when more than one rule applies.
  - Preserve the reason as part of the historical run snapshot.
  - Expose target counts separately from skipped/incompatible counts.
- [x] Restrict the reference gallery to established people
  (`is_provisional = 0`) with at least one approved reference.
- [x] Make ranking deterministic.
  - Sort by similarity descending.
  - Then margin descending.
  - Then support count descending.
  - Then person ID ascending.
  - Add tests with equal similarities and equal margins.
- [x] Handle incompatible target/reference embeddings per face.
  - Validate embedding dimension, dtype, normalization, and pipeline identity.
  - Skip incompatible targets and record the skip reason.
  - Do not fail the complete run because one face or reference is malformed.
- [x] Prevent a target face from matching against itself when it is also an
  approved reference for the candidate person.
- [x] Make candidate-row deletion behavior safe.
  - Add appropriate `ON DELETE` behavior or an explicit invalidation/archive
    path for face and person removal.
  - Verify that analysis reprocessing, collection removal, and person removal
    preserve readable historical runs and do not fail on foreign keys.
  - Keep historical rows auditable while removing them from current results.
- [ ] Add migration tests for the new target/eligibility data and deletion
  behavior.

## Phase 2: Provenance, stale state, and run lifecycle

- [x] Replace the count/max-timestamp reference version with a stable
  reference-set snapshot/version.
  - Ensure additions, removals, replacements, and concurrent changes produce a
    distinct version.
  - Store the version used by both the job request and the completed run.
- [x] Validate the queued reference-set version before generation.
  - Either reject/requeue stale jobs or explicitly record that the run used a
    newer version.
  - Ensure coalescing compares scope, policy, pipeline, and reference version.
- [x] Make stale invalidation scope-aware where appropriate.
  - Do not stale unrelated collections or people unnecessarily.
  - Mark rows stale when references, pipeline provenance, policy, target face,
    or candidate person changes.
- [x] Add explicit invalidation handling when a referenced face or person is
  removed or becomes unusable.
- [x] Implement cancellation semantics.
  - Detect context cancellation during target processing.
  - Finish the run as `cancelled`, retain partial history safely, and audit it.
  - Ensure cancellation does not leave current rows from an incomplete run.
- [x] Make retries idempotent.
  - Decide whether retries resume a run or create a new run.
  - Guarantee no duplicate current rows after retry or worker restart.
- [ ] Audit run creation, start, completion, cancellation, retry, and failure
  as separate lifecycle events with useful scope and provenance.

## Phase 3: Scheduling and worker behavior

- [x] Remove clustering from the reference-reprocess path caused by candidate
  discovery.
  - Reference reprocessing may enqueue possible-candidate discovery.
  - Possible-candidate discovery must never enqueue or run clustering.
  - Preserve any independent clustering behavior only where another explicit
    workflow requires it.
- [x] Make duplicate-request coalescing atomic.
  - Add a database-backed uniqueness strategy or transactional insert for
    equivalent pending requests.
  - Test concurrent enqueue attempts.
- [x] Add incremental progress reporting for processed targets, retained
  candidates, skipped targets, and the run ID.
- [x] Persist a candidate-specific job summary, not only progress data.
  - Include job ID, run ID, scope, target count, candidate count, skipped count,
    and final status.
- [ ] Verify scope behavior for person, collection, and all-accessible-data
  requests, including empty and unauthorized scopes.
- [ ] Confirm the 60-second reference-reprocess debounce remains intact while
  candidate discovery is coalesced separately.

## Phase 4: CLI and web review behavior

- [x] Enforce current-row selection when assigning from a candidate.
  - Require `is_current = 1`.
  - Confirm the candidate run is valid for the face and accessible collection.
  - Reject stale or superseded candidate IDs.
- [x] Include both candidate row ID and run ID in the assignment audit event,
  along with the prior assignment state and selected person.
- [ ] Display candidate generated time, provenance, and stale/current state on
  face cards and provisional/unknown/ambiguous review pages.
- [x] Add review filters for stale versus current results.
- [ ] Ensure candidate displays consistently include rank, similarity, margin,
  support, quality, detection, evidence, retention reason, and run link.
- [ ] Clearly label candidates as possible matches and keep the default action
  as review rather than acceptance.
- [ ] Add authorization tests for candidate run pages, candidate links, and
  assignment actions across collection permissions.

## Phase 5: Test coverage

### Generator and ranking

- [ ] Test unknown, ambiguous, unassigned, provisional, assigned-system, and
  assigned-user eligibility.
- [ ] Test provisional groups of 1, 5, and 6 faces.
- [ ] Test don't-track exclusion and explicit inclusion.
- [ ] Test assigned-system exclusion and explicit inclusion.
- [ ] Test established-person-only galleries and empty galleries.
- [ ] Test missing, malformed, incompatible, and mismatched embeddings.
- [ ] Test similarity-floor retention, top-ranked below-floor retention, and
  configured candidate limits.
- [ ] Test all deterministic ranking tie-breakers.
- [ ] Test that assignments and cluster memberships are unchanged.

### Persistence and lifecycle

- [ ] Test eligibility reason and zero-candidate target persistence.
- [ ] Test historical rows remain readable after a newer run.
- [ ] Test current-row replacement and scope-aware stale marking.
- [ ] Test reference, pipeline, policy, face, and person invalidation.
- [ ] Test cancellation, failure, retry, and partial-run cleanup.
- [ ] Test analysis reprocessing and deletion with candidate rows present.

### Jobs and integration

- [ ] Test atomic duplicate-request coalescing, including concurrent requests.
- [ ] Test reference-reprocess debounce and confirm no clustering is started.
- [ ] Test progress and candidate-specific job summary output.
- [ ] Test person, collection, and all scopes with authorization boundaries.

### CLI and web

- [ ] Test CLI scheduling, run/status/detail output, JSON envelopes, and
  pagination.
- [ ] Test face candidate output, including stale/current state and provenance.
- [ ] Test candidate display and filters in all review contexts.
- [ ] Test stale candidate assignment rejection.
- [ ] Test candidate assignment creates `assigned-user`, preserves the normal
  audit event, and records candidate row/run IDs.
- [ ] Test CSRF, collection authorization, and protection of user-confirmed
  assignments.

## Phase 6: Verification and rollout

- [x] Run `gofmt -w` on changed Go files.
- [x] Run `go test ./...`.
- [x] Run `go vet ./...`.
- [x] Run `git diff --check`.
- [ ] Build a private fixture containing:
  - established people with one and multiple approved references;
  - singleton provisional people;
  - provisional groups with five and six faces;
  - unknown, ambiguous, unassigned, assigned-system, assigned-user, and
    don't-track faces;
  - incompatible and missing embeddings;
  - equal-score candidate ties.
- [ ] Confirm candidate generation changes neither assignment rows/events nor
  cluster rows/memberships.
- [ ] Exercise analysis reprocessing and collection/person deletion after
  candidate generation.
- [ ] Compare ranks 1–5 against manually reviewed labels and record precision,
  coverage, and failure cases.
- [ ] Measure runtime and database growth on a representative private dataset.
- [ ] Document whether automatic post-reference scheduling should be enabled
  by default based on those measurements.
- [ ] Update `docs/SPEC.md`, `docs/DESIGN.md`, and usage documentation if the
  finalized lifecycle, target persistence, or stale semantics change.

## Completion criteria

The gap-closure work is complete when every unchecked item above is either
implemented and covered by tests or explicitly documented as an accepted
scope decision, and the private fixture and rollout verification produce
evidence that candidate discovery is review-only, deterministic, auditable,
retry-safe, and safe alongside reprocessing and deletion.
