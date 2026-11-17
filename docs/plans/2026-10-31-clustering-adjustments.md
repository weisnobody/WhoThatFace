# Conservative Clustering Adjustments

## Summary

Reduce large mixed family clusters by replacing the current permissive
transitive grouping behavior with conservative, reproducible clustering while
preserving reviewability and historical runs.

## Behavior

- Keep clustering runs immutable and make rebuilt runs reproducible.
- Prefer smaller high-confidence groups over long chains of individually
  acceptable links.
- Treat mixed clusters and low-purity groups as review-required rather than
  assigning them wholesale.
- Preserve user-confirmed assignments as fixed identity anchors.
- Keep unresolved faces groupable, but allow borderline faces to remain in
  smaller groups or singleton review units.
- Provide an explicit full-clustering rebuild operation; do not add subset
  clustering until active-run invalidation semantics are defined.

## Implementation

- Evaluate the current mutual-kNN `K=40`, threshold `0.45`, and connected
  component behavior against the observed 1,021-face and 89-face clusters.
- Use the conservative `mutual-knn-v2-anchor-safe` strategy by default with
  `K=10` instead of `K=40`.
- Reject graph edges between faces already assigned to different people, while
  allowing unknown faces to attach to either identity for review.
- Keep explicit mixed-cluster handling for conflicts that remain among
  unresolved faces.
- Persist algorithm/version/configuration metadata on every run.
- Expose rebuild results without deleting historical runs.
- Update cluster review summaries and filters to surface purity, consistency,
  connectivity, and mixed status.

## Tests

- Regression test long similarity chains that currently collapse into one
  cluster.
- Verify known people do not get merged into a high-confidence single-person
  group.
- Verify uncertain faces remain reviewable.
- Verify user-confirmed faces remain fixed across rebuilds.
- Verify historical and active run behavior remains correct.

## Assumptions

- Recovery and unresolved-face reassessment are completed before judging the
  quality of a new clustering configuration.
- No automatic person merge or split is introduced by this change.
