# Correct the Remaining Photo Review Gaps

## Summary

Address the six confirmed issues in the photo review implementation, align
defaults with the people page, support candidate-only representative images,
and replace the incomplete regression coverage. Update the task/checklist
documents so they accurately describe the delivered behavior.

## Implementation Changes

- **Photo detail actions and numbering**
  - Add the missing `incorrect-assignment` action for actionable faces.
  - Keep Reference and Don’t Track actions removed.
  - Preserve the required unresolved-face order: candidates → cluster/No
    cluster → actions → assignment.
  - Number list-card people from each face’s persisted `face_index`, not slice
    position.
  - Add an explicit fallback person label and link whenever a person ID exists.

- **Representative images**
  - Keep representative reads restricted to people related to at least one face
    in an accessible collection.
  - Extend automatic representative selection beyond current assignments to
    include:
    1. current assigned faces;
    2. active cluster proposed-person faces;
    3. recognition-candidate faces;
    4. current possible-candidate faces.
  - Preserve existing assigned-face priority and use deterministic
    rank/quality/detection/face-ID ordering for candidate relationships.
  - Generate and cache only the cropped representative image; never expose the
    source photo through the representative endpoint.
  - Preserve existing viewer/editor restrictions for selecting and clearing
    representatives.

- **Photo-list defaults and no-face behavior**
  - Default the list to photos with at least one completed face.
  - Define `faces=all` as all accessible photos, `faces=with` as photos with
    faces, and `faces=without` as photos without faces.
  - Keep the explicit `without` option available.
  - Match people-page review defaults: Don’t Track is excluded by default;
    normal review faces, including Unknown and Possibles, remain visible by
    default.
  - When explicit status filters are submitted, match any selected status
    using OR semantics; Don’t Track appears only when explicitly selected.
  - Preserve selected/default values in controls and pagination URLs.

- **Regression coverage**
  - Add search tests for:
    - default, all, with-face, and without-face behavior;
    - Don’t Track, Unknown, and Possibles filters independently and in OR
      combinations;
    - assignment OR combinations;
    - proposed, recognition-candidate, and possible-candidate person matching;
    - accessible versus restricted collection scope;
    - every sort direction and stable ties;
    - aggregate counts and SQL page boundaries.
  - Add web tests for:
    - incorrect-assignment rendering;
    - assigned cards containing no candidates, cluster metadata, or actions;
    - unresolved cards and required control ordering;
    - face-index-based numbering;
    - default control state and filter preservation;
    - restricted duplicate-collection summaries;
    - candidate-only representative generation and unrelated-person denial;
    - viewer/editor representative behavior;
    - no original-photo leakage through representative reads.
  - Use synthetic database rows only.

- **Documentation and checklist cleanup**
  - Update the missed-changes plan and task list to record the resolved no-face
    semantics and representative-image behavior.
  - Correct any checklist entries that currently claim coverage without
    corresponding tests.
  - Keep the future-work items for rating sorting and display identity
    unchanged.

## Validation

Run:

```sh
go test ./internal/search ./internal/web
go test ./...
git diff --check
```

Acceptance requires all six findings to have regression tests, candidate-only
representatives to return an image when an authorized related face exists,
unrelated representative requests to remain forbidden, no-face defaults to
match the documented behavior, and actionable photo faces to retain the
complete supported review workflow.

## Assumptions

- The selected no-face policy is: exclude no-face photos by default;
  `all` and `without` remain explicit modes.
- Candidate-only representative images should be generated from an authorized
  related face and stored as a cropped representative.
- Existing original-photo authorization remains path/collection based and is
  not broadened.
- Rating sorting and user/collection-specific display identity remain out of
  scope.
