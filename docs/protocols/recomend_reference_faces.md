# Recommend Reference Faces

## Purpose

After faces have been manually confirmed as belonging to a person, the system
can recommend which faces should be promoted to that person's reference set.

Recommendations should remain user-approved. A poor reference face can reduce
future recognition quality, so the system must not automatically promote faces
without review.

## Selection principles

Recommended reference faces should be:

- High quality.
- Reliably detected.
- Representative of the person's appearance.
- Distributed across different photos and conditions.
- Preferably from different scenes or time periods.
- Clearly separated from other people.
- Not near-duplicates from the same burst or scene.

The highest recognition score alone is not sufficient. A list made entirely of
near-identical frontal photos may have high scores but provide poor coverage of
the person's appearance.

## Candidate eligibility

Start with faces that have been manually confirmed as a specific person.

Normally exclude:

- Faces labeled unknown, ambiguous, or false detection.
- Faces in mixed or uncertain clusters unless individually confirmed.
- Faces below the configured quality threshold.
- Faces below the configured detection-confidence threshold.
- Near-duplicate faces from the same photo sequence.
- Faces with severe blur, occlusion, or unsuitable cropping.

Reference faces may initially have been labeled as evaluation faces. Promotion
to reference status should be an explicit user-approved action.

## Recommendation score

A recommendation score can combine:

```text
reference suitability =
    face quality
  + detection confidence
  + similarity to the person's embedding center
  + cluster consistency
  + image and face size
  - duplicate similarity
  - mixed-cluster risk
```

The components should remain visible in the review interface so the user can
understand why a face was recommended.

### Quality and detection

Quality and detection confidence indicate whether the embedding and bounding
box are likely to be reliable. They are useful exclusion and prioritization
signals, but do not prove that a face is the correct person.

### Similarity to the person's center

Calculate an embedding centroid or medoid from the person's confirmed faces.
Faces close to the center are generally representative and less likely to be
outliers.

The centroid should not be the only selection criterion because it may favor a
single common pose or lighting condition.

### Cluster consistency

Faces from a clean, internally consistent cluster are stronger candidates than
faces from a mixed or uncertain cluster. In discovery mode, cluster quality is
estimated from internal similarity and graph structure. During evaluation, the
manually assigned labels may also be used to measure actual cluster purity.

### Diversity and duplicate avoidance

After ranking candidate faces, select a diverse subset rather than the top
faces only. The selection should avoid choosing multiple nearly identical faces
from the same photo burst or scene.

Useful diversity signals include:

- Different source photos.
- Different clusters when those clusters have been confirmed as the same person.
- Embedding distance from already selected references.
- Different image dates when available.
- Different pose, lighting, or apparent appearance when available.

## Suggested selection process

1. Start with manually confirmed faces for one person.
2. Exclude low-signal, ambiguous, mixed-cluster, and duplicate candidates.
3. Calculate the person's embedding center or medoid.
4. Rank remaining faces using quality, detection confidence, centrality, and
   cluster consistency.
5. Select a diverse subset using a greedy diversity step.
6. Show the recommendations and their supporting metrics to the user.
7. Let the user approve, reject, or substitute faces.
8. Promote only approved faces to the reference role.
9. Rerun recognition after the reference gallery changes.

## Initial reference-set recommendations

For a person with sufficient confirmed faces, the system should initially
recommend a small set such as:

- One high-quality representative face.
- One different pose or view.
- One face from different lighting or an additional time period.
- One additional face when the person's appearance varies substantially.

The exact number should remain configurable. More references are not always
better: redundant or poor-quality faces can add noise and increase processing
cost.

## Review interface

The recommendation view should show, for each candidate:

- Photo and face crop.
- Current person assignment.
- Face quality.
- Detection confidence.
- Similarity to the person's center.
- Cluster identifier and cluster consistency.
- Source photo and capture date when available.
- Reason for inclusion or exclusion.
- Whether the face is already a reference.

Example:

| Face | Quality | Detection | Similarity to center | Cluster | Recommendation |
|---|---:|---:|---:|---:|---|
| Photo A | 0.91 | 0.88 | 0.96 | 12 | Strong candidate |
| Photo B | 0.87 | 0.85 | 0.94 | 15 | Good diversity |
| Photo C | 0.52 | 0.61 | 0.97 | 12 | Exclude: low signal |

## Effect on later recognition

Adding approved faces to a person's reference set changes the comparison
gallery. It may change:

- Similarity scores.
- The winning candidate.
- The margin between candidates.
- Whether a face is classified as unknown, review, or high-confidence.

The face embedding itself does not change. Existing predictions must be
recalculated after the reference gallery changes.

## Safety rules

- Recommendations must not silently promote faces.
- Mixed clusters must not be promoted wholesale.
- A user must be able to reject individual recommendations.
- The system should retain the reason and timestamp for each promotion.
- Reference changes should be reversible.
- Evaluation results should distinguish faces used as references from faces held
  out for evaluation.

