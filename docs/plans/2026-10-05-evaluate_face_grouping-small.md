# Plan: Evaluate Small-Dataset Face Grouping

## Objective

Determine which `k` value provides the best balance for a mutual-k-nearest-
neighbor face grouping graph on the existing reviewed sample dataset.

The candidate values are:

- `k=5`
- `k=10`
- `k=20`
- `k=40`

The result should identify a default `k` for the next grouping prototype and
document the accuracy, runtime, resource use, and expected manual review effort.

## Dataset

Use the existing fully reviewed experiment:

```text
datasets/samples/experiment.sqlite3
```

The dataset currently contains approximately 1,060 detected faces, 1,043
person-labeled faces, 57 labeled people, 17 unknown faces, and both
reference-backed and non-reference people.

The evaluation must use the manually assigned labels only for scoring. Labels
must not be used to construct the graph or influence neighbor selection.

## Scope

### Included

- Exact batched similarity search for the small dataset.
- Mutual-k-nearest-neighbor edge construction.
- A configurable minimum cosine similarity threshold.
- Connected-component provisional groups.
- Comparison of `k=5,10,20,40`.
- Accuracy, graph, performance, and review-effort metrics.
- Results split into all faces, reference-backed people, and non-reference
  people where meaningful.
- Machine-readable and human-readable reports.

### Deferred

- ANN indexes.
- Production clustering persistence.
- User-facing cluster editing, merging, and splitting.
- Alternative clustering algorithms.
- Automatic promotion of cluster members to reference faces.

## Fixed evaluation configuration

To isolate the effect of `k`, hold these settings constant across all runs:

- Embeddings: existing stored face embeddings.
- Distance: cosine similarity.
- Edge direction: add an edge only when both faces include each other in their
  top-`k` neighbor lists.
- Minimum edge similarity: configurable, default `0.45` based on the existing
  provisional-grouping baseline.
- Grouping: connected components of the mutual-neighbor graph.
- Self-neighbors: excluded.
- Tie handling: deterministic, using face ID as the final ordering key.
- Processing order: deterministic face-ID order for the initial comparison.

The command should allow the similarity threshold to be overridden so a later
sensitivity run can compare thresholds without changing the implementation.

## Implementation tasks

### 1. Build the exact neighbor matrix or top-k results

- Load face IDs, embeddings, labels, and reference status.
- Normalize embeddings if required by the existing cosine implementation.
- Calculate exact similarities in batches without requiring a dense full matrix.
- Retain only the top 40 neighbors needed by the largest candidate `k`.
- Exclude the face itself.
- Record elapsed time and peak memory where practical.

### 2. Construct mutual-kNN graphs

For each candidate `k`:

1. Select the top `k` neighbors for every face.
2. Discard directed relationships below the minimum similarity threshold.
3. Keep an edge only when the relationship is mutual.
4. Build connected components using the remaining undirected edges.
5. Assign stable cluster IDs for reporting.

The implementation should report isolated faces as one-face clusters rather
than silently dropping them.

### 3. Calculate grouping metrics

Report the following for each `k`:

- Number of input faces.
- Number of clusters.
- Number of singleton clusters.
- Minimum, median, mean, and maximum cluster size.
- Number and percentage of mixed-person clusters.
- Cluster purity.
- Pair precision.
- Pair recall.
- Pair F1 score.
- Same-person fragmentation.
- Unknown-face cluster participation.

Calculate metrics for:

- All faces.
- Faces belonging to people with reference faces.
- Faces belonging to people without reference faces.

Unknown faces should be included in operational cluster counts, but should be
reported separately from known-person accuracy metrics.

### 4. Estimate review effort

For each candidate `k`, report:

- Number of clusters requiring a person assignment.
- Number of faces in mixed clusters.
- Number of clusters for each manually known person.
- Number of known people split across multiple clusters.
- Number of singleton or very small clusters.
- Estimated number of user assignments needed to name all clusters.

The report should make clear that high purity alone is insufficient: a method
that puts every face in its own cluster can have excellent purity but poor
review usability.

### 5. Measure performance

Record for each candidate run:

- Neighbor-search time.
- Graph-construction time.
- Connected-component time.
- Total elapsed time.
- Peak memory if available.
- Number of candidate neighbor relationships.
- Number of mutual edges after thresholding.
- Output graph size.

Neighbor search may be shared across `k` values because the largest required
value is `k=40`. The report should distinguish shared work from per-`k` work.

### 6. Check order stability

The graph algorithm itself should be order-independent. Add a validation run
that shuffles input order using at least three deterministic seeds and confirms
that cluster membership is unchanged apart from cluster IDs.

If the implementation uses any order-dependent operation, report that as a
failure rather than hiding it behind reordered cluster IDs.

### 7. Write reports

Create a timestamped output directory under the experiment directory containing:

- `grouping_results.json` with configuration, metrics, and timings.
- `grouping_results.csv` with one summary row per `k` and threshold.
- `clusters-k5.jsonl`, `clusters-k10.jsonl`, `clusters-k20.jsonl`, and
  `clusters-k40.jsonl` with cluster membership and summary information.
- `report.md` with the comparison, interpretation, and recommendation.
- `report.html` with sortable result tables and browsable cluster summaries.

The report should recommend a default `k` using a documented rule, not a single
metric. The initial rule should prefer high pair precision and low mixed-cluster
rate, then use pair recall and review effort to break ties.

## Tests

Add unit tests for:

- Excluding self-neighbors.
- Deterministic top-k tie handling.
- Mutual-edge filtering.
- Similarity threshold filtering.
- Connected-component formation.
- Isolated-face handling.
- Stable cluster membership under input reordering.
- Purity, pair precision, pair recall, and pair F1 calculations.
- Correct separation of reference-backed and non-reference populations.

Use a small synthetic embedding fixture with known expected neighbors and
clusters. The existing experiment dataset should be used for an integration
test of the end-to-end command.

## Acceptance criteria

The plan is complete when:

- All four candidate `k` values can be evaluated with one command.
- The same neighbor results are reused safely across the four `k` values.
- Mutual-neighbor edges and connected components are deterministic.
- Reports include accuracy, cluster counts, review effort, timing, and memory
  information.
- Results are split between reference-backed and non-reference people.
- The generated report identifies a recommended default `k` and explains the
  tradeoff.
- Unit and integration tests pass.

## Suggested command shape

The exact command name may be finalized during implementation, but it should
support a form similar to:

```sh
python prototypes/model_settings_review/src/review_tool.py group-benchmark \
  --experiment datasets/samples \
  --k-values 5,10,20,40 \
  --similarity-threshold 0.45
```

