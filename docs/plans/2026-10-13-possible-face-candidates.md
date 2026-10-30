# Possible Face Candidates Implementation Plan

## Objective

Implement the complete feature described in
[`docs/possible_face_candidates.md`](../possible_face_candidates.md). The
feature must identify up to five likely established people for eligible
unknown, ambiguous, unassigned, and small provisional face groups without
changing assignments or cluster membership.

## Constraints and decisions

- Use approved reference faces only; recommended references are not matching
  inputs.
- Use the existing exact cosine-similarity matcher for the first
  implementation.
- Retain up to five candidates per target face.
- Use one approved reference as the minimum required gallery for a candidate
  person.
- Default provisional-person limit is five faces.
- Exclude `assigned-user` and don't-track faces by default.
- Do not start clustering as a side effect.
- Preserve historical runs and candidate rows.
- Keep candidate discovery separate from ordinary recognition assignments.

## Phase 1: Configuration and data model

 - [x] Add configuration fields and defaults for:
   - maximum provisional faces per target person (`5`);
   - maximum candidates per face (`5`);
   - minimum candidate similarity (`0.25`);
   - inclusion of `assigned-system` faces (`false`);
   - inclusion of don't-track faces (`false`);
   - default discovery scope and stale-result policy.
 - [x] Add a migration for a possible-candidate run table containing:
   - UUIDv7 ID;
   - scope and scope ID;
   - policy/configuration snapshot;
   - pipeline/model and reference-set provenance;
   - status and timestamps;
   - target and result counts;
   - error summary.
 - [x] Add a migration for candidate rows containing:
   - run ID, target face ID, candidate person ID, and rank;
   - similarity, margin, support, quality, and detection;
   - evidence category and retention reason;
   - current/stale state and timestamps.
 - [x] Add indexes and foreign keys for target-face, candidate-person, current-row,
   and run lookups.
 - [x] Add Go models and stores for creating runs, writing rows, marking prior
   rows stale, retrieving current candidates, and retrieving run details.

## Phase 2: Eligibility and candidate generation

 - [x] Implement a deterministic target selector that records the reason each face
   was eligible:
   - unknown, ambiguous, or unassigned;
   - provisional assignment;
   - provisional person with one through five provisional faces.
 - [x] Apply exclusions for assigned-user, don't-track, missing embeddings, and
   incompatible pipeline/model provenance.
 - [x] Add explicit options for including assigned-system and don't-track faces.
 - [x] Load candidate people with at least one approved reference face and group
   their compatible reference embeddings.
 - [x] Evaluate each target face against every candidate person with the existing
   matcher.
 - [x] Calculate strongest similarity, second-best similarity, margin, support,
   quality, detection, evidence level, rank, and retention reason.
 - [x] Retain candidates meeting the similarity floor or the top-five rule.
 - [x] Apply deterministic ordering:
   similarity descending, margin descending, support descending, person ID
   ascending.
 - [x] Persist all results under one run, including zero-candidate target faces.
 - [x] Ensure the implementation never calls assignment mutation methods or
    modifies clusters.

## Phase 3: Jobs and worker integration

 - [x] Add a `possible-candidates` job kind and payload contract.
 - [x] Support scopes for one person, one collection, and all accessible data.
 - [x] Support optional inclusion of assigned-system and don't-track faces.
 - [x] Add progress reporting for eligible faces processed, candidates retained,
   and skipped faces.
 - [x] Coalesce duplicate pending requests for the same scope, options, pipeline,
   and reference-set version.
 - [x] Make retries safe and idempotent. A retry must create a new run or resume
   the existing run without leaving duplicate current rows.
 - [x] Preserve the existing 60-second reference-reprocess debounce. Reference
   changes may request candidate discovery, but candidate discovery must not
   trigger clustering.
 - [x] Mark results stale when approved references, pipeline identity, or relevant
   policy settings change.
 - [x] Add job summaries with target counts, candidate counts, skipped counts, and
   the run ID.

## Phase 4: CLI

 - [x] Add commands equivalent to:

   ```text
   face-me-bro job possible-candidates --person-id PERSON_ID
   face-me-bro job possible-candidates --collection-id COLLECTION_ID
   face-me-bro job possible-candidates --all
   face-me-bro face candidates --face-id FACE_ID
   ```

 - [x] Add options for candidate limit, provisional-person limit, similarity floor,
   include-assigned-system, and include-dont-track.
 - [x] Add list/get/status commands for possible-candidate runs if the existing run
   command structure can accommodate them; otherwise add a dedicated
   `candidate-run` command group.
 - [x] Use the standard table headers, pagination, JSON envelope, UUID suffix
   display, and error conventions.
 - [x] Include the run ID, scope, target count, candidate count, and stale/current
   state in command output.
 - [x] Update command help, sample configuration, and usage documentation.

## Phase 5: Review web interface

 - [x] Add current possible candidates to face-card data and rendering.
 - [x] Display candidate name/link, rank, similarity, margin, support, quality,
   detection, evidence, and generated time.
 - [x] Add filters for:
   - faces with possible candidates;
   - candidate person;
   - evidence level;
   - stale versus current results;
   - unknown, ambiguous, unassigned, and provisional target states.
 - [x] Add provisional-person-page support for showing candidates on each face and
   filtering to small provisional groups.
 - [x] Add unknown/ambiguous review pages or extend the existing face review page
   to show possible candidates.
 - [x] Link candidate people to their person pages and candidate runs to run-detail
   pages.
 - [x] Add actions to assign a face to a selected candidate as `assigned-user`,
   reject/leave unchanged, mark unknown, mark ambiguous, or assign another
   person.
 - [x] Require normal CSRF, collection authorization, audit logging, and existing
   bulk-assignment safeguards.
 - [x] Clearly label the result as a possible candidate and not a confirmed or
   automatic assignment.

## Phase 6: Audit, stale results, and operational behavior

 - [x] Audit candidate-run creation, completion, cancellation, retry, and failure.
 - [x] Audit user assignments made from candidate results, including candidate run
   and row IDs where available.
 - [x] Define and implement stale-result behavior when references change while a
   run is active.
 - [x] Ensure old run pages remain viewable and identify superseded/stale data.
 - [x] Ensure don't-track filtering is consistent across CLI, web, and persistence.
 - [x] Ensure candidate generation is local-only and does not log embeddings or
   private image data.

## Phase 7: Tests

### Unit tests

- [x] Eligibility for every assignment state.
- [x] Singleton and one-to-five provisional-person limits.
- [x] Exclusion and opt-in behavior for assigned-system and don't-track faces.
- [x] Missing embeddings and incompatible pipeline handling.
- [x] Candidate ranking, tie-breaking, margin, support, and similarity floor.
- [x] Top-five retention below normal assignment thresholds.
- [x] Evidence-category calculation and retention reasons.

### Store and migration tests

- [x] Run and candidate-row creation.
- [x] Historical rows retained while current rows are replaced.
- [x] Foreign keys and indexes.
- [x] Stale-result marking and provenance mismatch handling.

### Job and integration tests

- [x] Person, collection, and all scopes.
- [x] Empty galleries and zero-candidate faces.
- [x] Idempotent retry behavior.
- [x] Duplicate-request coalescing.
- [x] Reference-reprocess integration without clustering.
- [x] Job progress and summary output.

### Web and CLI tests

- [x] Candidate display and filters.
- [x] Candidate links and stale labels.
- [x] Assignment action from a candidate creates `assigned-user` and an audit row.
- [x] User-confirmed assignments remain unchanged during discovery.
- [x] Authorization, CSRF, don't-track defaults, pagination, and JSON output.

## Verification and rollout

 - [x] Run `gofmt`, `go test ./...`, `go vet ./...`, and `git diff --check`.
 - [ ] Run the feature against a fixture containing:
   - established people with one and multiple references;
   - singleton provisional people;
   - provisional groups with five and six faces;
   - unknown, ambiguous, assigned-system, assigned-user, and don't-track faces.
 - [ ] Confirm no assignment or cluster rows change during candidate generation.
 - [ ] Compare candidate output with manually reviewed labels and record precision
   at ranks 1 through 5.
 - [ ] Verify runtime and database behavior on the representative dataset before
   enabling automatic post-reference scheduling by default.
 - [x] Update `docs/SPEC.md` and `docs/DESIGN.md` with the finalized data model,
   scheduling rules, and review semantics before calling the feature complete.
