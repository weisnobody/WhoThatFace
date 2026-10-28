# Design and Specification Items to Review

This document captures changes that should be reviewed before updating
`docs/DESIGN.md` and `docs/SPEC.md`. It does not itself change either document.

## Summary

The main design question is how discovery-first grouping, recognition, and
reference-face selection fit together.

The benchmark evidence indicates:

- Reference-based recognition performs well once people have reference faces.
- Recognition without reference faces performs poorly, so discovery grouping
  is important rather than optional.
- Grouping and recognition are separate operations and should not be conflated.
- The current prototype direction is a mutual-k-nearest-neighbor graph with
  connected components.
- The small benchmark compared `k=5,10,20,40` at similarity threshold `0.45`.
- `k=40` produced the highest overall pair F1 on the current dataset, while
  `k=20` was very similar with slightly fewer edges.
- The current dataset is adequate for correctness and initial parameter
  selection, but not for large-library scalability conclusions.
- Low quality and low detection confidence should prioritize review, but should
  not by themselves be treated as evidence that a face is incorrectly grouped.

## 1. Strengthen discovery-first as the primary workflow

`SPEC.md` already mentions discovery-first, but it should explicitly state that:

- Many people will initially have no reference faces.
- Discovery grouping is the normal way to build reference sets over time.
- New people and new appearances may appear after the initial library is
  processed.
- Reference-based recognition improves as users confirm and approve faces.

Relevant sections:

- `SPEC.md` 1.1 MVP scope
- `SPEC.md` 10.4 Discovery-first workflow
- `SPEC.md` 11 Clustering

## 2. Clarify when a face becomes a reference

The current wording says that confirmed and manually assigned faces become
future matching examples. This should be reviewed because the preferred workflow
now distinguishes confirmation from reference promotion:

- A confirmed face becomes eligible for reference recommendation.
- The user approves selected faces as references.
- Only approved reference faces enter the recognition gallery.
- Poor-quality, unusual, duplicate, or mixed-cluster faces should not be
  promoted automatically.

Potentially affected areas:

- Recognition and identity assignment in `DESIGN.md`.
- Confirmation versus automatic assignment in `DESIGN.md`.
- `SPEC.md` 10.3 Assignment policy.
- `SPEC.md` 10.4 Discovery-first workflow.
- `SPEC.md` 12 Data model.

## 3. Define the relationship between recognition and grouping

Recognition and grouping answer different questions:

- Recognition asks whether a face matches a known person.
- Grouping asks which faces look similar to one another.

The specification should define:

- Which faces are eligible for production grouping.
- Whether high-confidence recognized faces are excluded.
- Whether reference faces are excluded.
- Whether grouping can run on a user-selected subset.
- Whether a face may appear in both recognition and grouping results.
- How mixed clusters are handled.

The benchmark grouped all faces to evaluate grouping quality. That does not
necessarily mean production grouping should always process all faces.

## 4. Select the grouping algorithm direction

The current specification defers the clustering algorithm. The prototype now
has a concrete direction:

- Build a k-nearest-neighbor similarity graph.
- Keep mutual-nearest-neighbor edges.
- Apply a minimum similarity threshold.
- Form provisional groups using connected components.
- Compare `k=5,10,20,40`.

At similarity threshold `0.45`, the current benchmark produced:

| k | Clusters | Pair precision | Pair recall | Pair F1 |
|---:|---:|---:|---:|---:|
| 5 | 305 | 94.18% | 30.27% | 45.82% |
| 10 | 188 | 90.04% | 63.19% | 74.26% |
| 20 | 129 | 90.97% | 82.65% | 86.61% |
| 40 | 125 | 90.96% | 83.76% | 87.21% |

The report currently recommends `k=40` by pair F1, although `k=20` is close
and has slightly fewer edges.

Review whether to adopt mutual-kNN as the MVP grouping direction while keeping
`k` and the similarity threshold configurable.

## 5. Define low-signal review prioritization

The grouping benchmark now tracks:

- Low-quality faces.
- Low-detection-confidence faces.
- Faces meeting either condition.
- Faces meeting both conditions.
- Low-signal faces inside mixed-person clusters.

These are review-priority signals, not proof of incorrect grouping. The
specification should distinguish them from recognition thresholds:

- Recognition thresholds determine unknown, review, or high-confidence
  decisions.
- Review thresholds determine which faces deserve closer inspection.
- A low-signal face may still be correctly grouped.
- A high-signal face may still be incorrectly grouped.

The current prototype defaults to quality `<0.70` or detection confidence
`<0.70`, both configurable.

## 6. Define scale boundaries and ANN transition

The current performance target is expressed mainly in image counts, while the
grouping workload is driven by face counts.

Approximate guidance:

| Faces | Recommendation |
|---:|---|
| Under 10,000 | Exact batched search |
| 10,000–50,000 | Exact search is practical for occasional rebuilds |
| 50,000–100,000 | Compare exact search with ANN |
| 100,000–250,000 | ANN is likely worthwhile |
| 250,000+ | ANN should be the default |

ANN should replace the nearest-neighbor search implementation while preserving
the mutual-kNN graph model. The transition should be evaluated using neighbor
recall, grouping metrics, runtime, memory, and incremental update cost.

Relevant sections:

- `DESIGN.md` Similarity search.
- `SPEC.md` 16 Performance targets.
- `SPEC.md` 20 Model-selection prototype.
- `SPEC.md` 22 Open decisions.

## 7. Review the clustering data model

The existing data model mentions clustering runs, provisional clusters, and
cluster memberships. Review whether it should explicitly store:

- Algorithm name and version.
- `k` and similarity threshold.
- Neighbor-search method.
- Mutual-edge policy.
- Graph and edge statistics.
- Cluster quality metrics.
- Cluster review state.
- Cluster-level assignments.
- Individual face overrides.
- Recomputed clustering runs.

Relevant sections:

- `SPEC.md` 11 Clustering.
- `SPEC.md` 12 Data model.
- SQLite responsibilities in `DESIGN.md`.

## 8. Review the reference recommendation data model

Reference recommendations may need their own persisted or auditable state.
Review whether to store:

- Recommendation score.
- Quality and detection components.
- Similarity to the person's embedding center.
- Diversity reason.
- Cluster identifier.
- Recommendation timestamp.
- User approval or rejection.
- Promotion timestamp.
- Reference-set version used for the recommendation.

The minimum MVP may keep some recommendation details transient, but approved
reference changes should remain auditable and reversible.

## 9. Clarify cluster review versus face review

Review the behavior for:

- Assigning an entire pure cluster.
- Handling a mixed cluster.
- Excluding individual faces from a cluster assignment.
- Recommending references after a cluster is confirmed.
- Regenerating clusters after reference changes.
- Treating old cluster results after a new clustering run.

The current direction is that clusters remain provisional, mixed clusters remain
explicitly reviewable, and user-facing merge/split operations remain deferred.

## 10. Add grouping evaluation as a first-class protocol

The specification currently emphasizes recognition evaluation. It should also
reference:

- `docs/protocols/evaluate_face_grouping_options.md`
- `docs/plans/evaluate_face_grouping-small.md`

The evaluation categories should be separated into:

- Recognition accuracy.
- Grouping accuracy.
- Reference recommendation quality.
- Manual review effort.
- Scalability.

## 11. Record benchmark limitations

The current grouping benchmark is useful but limited:

- Approximately 1,060 faces.
- 57 labeled people.
- One primary dataset.
- Exact neighbor search.
- Fixed initial similarity threshold of `0.45`.
- No ANN comparison yet.
- No large-library performance results yet.
- Connected-component behavior remains dependent on graph connectivity and
  threshold selection.

The results should not be treated as final production calibration.

## 12. Revisit benchmark and open-decision wording

The existing model-selection benchmark covers an 86-image, 129-face dataset.
That should remain separate from:

- Recognition settings evaluation.
- Grouping evaluation.
- Reference-face recommendation evaluation.
- Large-library scalability evaluation.

The open-decisions section should be reviewed to distinguish decisions already
made from decisions still pending. Current pending items include:

- Final grouping `k`.
- Final grouping similarity threshold.
- ANN implementation and transition point.
- Production grouping candidate set.
- Reference recommendation policy.
- Final quality and detection review thresholds.

## Document-specific review list

### `docs/DESIGN.md`

Review whether to:

1. Distinguish confirmed faces from approved reference faces.
2. Add a discovery-grouping subsystem boundary.
3. Document mutual-kNN graph grouping as the current prototype direction.
4. Explain the difference between face-to-person recognition and face-to-face
   grouping.
5. Add graph/index responsibilities to similarity search.
6. Add ANN transition guidance.
7. Add reference recommendation as an application capability.
8. Define whether grouping receives all faces or a filtered candidate set.
9. Add cluster review and reference-promotion flows.
10. Link the grouping protocol and plan in documentation boundaries.

### `docs/SPEC.md`

Review whether to:

1. Strengthen discovery-first as the normal workflow.
2. Change automatic future-example wording to explicit reference eligibility or
   approval.
3. Define reference recommendation and approval.
4. Adopt mutual-kNN graph clustering as the current MVP prototype direction.
5. Keep `k` and similarity thresholds configurable.
6. Define the production grouping candidate set.
7. Define mixed-cluster handling.
8. Define low-signal review prioritization.
9. Add grouping metrics and review-effort requirements.
10. Add graph/index data-model requirements.
11. Add face-count and ANN scalability guidance.
12. Link the grouping evaluation protocol and plan.
13. Update open decisions to reflect decisions already made.
14. Clarify remaining decisions around `k`, thresholds, ANN, and reference
    selection.

## 15. Review user- and collection-specific display identity

The current person model provides one default display name and one UI-only
representative image. A future feature may allow display identity to vary by
viewer or collection without changing the underlying person identity.

Review whether to support:

- A user-specific name and representative image for a person.
- A collection-specific name and representative image for a person, managed
  by an administrator for that collection.
- A default name and representative image selected from user-provided values.
- Initial defaults based on the first name and representative image assigned by
  a user, with administrators able to change the defaults later.
- Clear precedence when a user-specific, collection-specific, and global
  person value all exist.
- Separate permissions for assigning personal overrides, managing collection
  overrides, and changing global defaults.
- Audit history, replacement, clearing, and fallback behavior for names and
  representative images.
- Privacy and access behavior when a representative image was derived from a
  face in a collection the viewer cannot access.

This feature is future work and is not part of the photo review page
alignment implementation.

