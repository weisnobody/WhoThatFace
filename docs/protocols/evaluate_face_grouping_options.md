# Face Grouping Implementation Options

## Purpose

This document describes implementation options for grouping face embeddings
into provisional person groups during the discovery-first workflow.

InsightFace produces face embeddings, but it does not create identity groups by
itself. A grouping algorithm must compare embeddings, form relationships, and
create provisional clusters that a user can review and name.

The grouping system must be evaluated separately from reference-based
recognition. A cluster may contain multiple people or split one person across
multiple clusters. Clusters must therefore remain provisional until reviewed.

## Evaluation measures

The grouping prototype should report:

- Cluster count.
- Cluster size distribution.
- Cluster purity.
- Pair precision.
- Pair recall.
- Mixed-person cluster count.
- Fragmentation per person.
- Number of isolated or noise faces.
- Review effort, including clusters and individual faces requiring action.

### Purity

For each cluster, identify the person occurring most often in that cluster.
Count those faces as correct and divide by the total number of manually
identified faces.

For example, if a cluster contains eight faces of Alice and two faces of Bob,
the cluster contributes eight correct faces toward purity. Purity measures
whether clusters are internally consistent, but does not measure whether one
person has been split across many clusters.

### Pair precision and recall

Treat pairs of faces as the basic relationship:

- A true-positive pair contains two faces manually assigned to the same person
  and placed in the same cluster.
- Pair precision measures how often faces placed together really belong
  together.
- Pair recall measures how often faces belonging to the same person were placed
  together.

High precision with low recall means the groups are clean but fragmented. High
recall with low precision means groups contain more mixed identities.

## Implementation options

### Full pairwise similarity graph

Compare every face embedding with every other embedding. Add an edge when
cosine similarity exceeds a threshold, then form groups from connected
components.

Advantages:

- Simple to understand and debug.
- Easy to visualize.
- Suitable for an initial prototype.
- Connected components are deterministic.

Risks:

- Chain effects can merge multiple people into one group.
- Requires `O(n²)` comparisons.
- A single threshold may not work equally well for all people.

For datasets of roughly 1,000 to 10,000 faces, a NumPy implementation may be
practical.

### Pairwise graph with stricter edge rules

Use a similarity graph but require additional evidence before creating an edge:

- Similarity exceeds a threshold.
- Both faces meet a quality threshold.
- Each face is among the other face's top-k neighbors.
- Similarity is sufficiently close to the local best match.

A mutual-nearest-neighbor rule adds an edge only when each face is among the
other's best matches.

This can improve precision and reduce accidental cross-person edges, but may
fragment people into multiple groups. Prototype values should include `k=5`,
`k=10`, and `k=20`.

### k-nearest-neighbor graph

Find the top `k` nearest faces for each face, add edges above a similarity
threshold, and cluster the resulting graph. Mutual-nearest-neighbor edges may
be used for a more conservative graph.

Advantages:

- More scalable than comparing every pair.
- Avoids many obviously unrelated comparisons.
- Compatible with approximate nearest-neighbor indexes later.

Risks:

- Relationships may be missed when `k` is too small.
- Results depend on the selected `k`.
- More complex than a full pairwise graph.

### DBSCAN

DBSCAN groups embeddings based on density using `eps` and `min_samples`.
Cosine distance should be used, or embeddings should be normalized before using
an equivalent distance measure.

Advantages:

- Does not require the number of people in advance.
- Can identify isolated faces as noise.
- Widely understood and straightforward to prototype.

Risks:

- A single density threshold may not work for all people.
- Different photo counts can cause fragmentation.
- Results are less transparent than an explicit similarity graph.

### HDBSCAN

HDBSCAN extends DBSCAN by finding clusters at multiple density levels.

Advantages:

- Handles clusters with different densities.
- Can identify uncertain or noisy faces.
- Avoids selecting one global density threshold.

Risks:

- More difficult to explain and tune.
- Adds a dependency.
- May split people with few examples.

### Agglomerative hierarchical clustering

Start with one group per face and repeatedly merge the closest groups until a
similarity or distance threshold is reached. Average or complete linkage are
the most relevant initial choices.

Advantages:

- Produces a hierarchy that can support interactive review.
- Allows groups to be inspected at different similarity levels.

Risks:

- More expensive than simple incremental grouping.
- Results depend on the linkage strategy.
- Early merges can affect later results.

### Graph community detection

Build a similarity graph and apply a community-detection algorithm such as
Leiden or Louvain.

Advantages:

- Designed for graph relationships.
- Can handle sparse and dense structures.

Risks:

- More difficult to explain and tune.
- Requires additional dependencies.
- Communities do not necessarily correspond to identities.

This is likely more complexity than is needed for the MVP.

### Incremental centroid grouping

The current prototype uses an online centroid grouper:

1. Process faces in order.
2. Compare each face with existing group centroids.
3. Add it to the closest group when similarity exceeds a threshold.
4. Otherwise create a new group.

This is fast and easy to understand, but is order-dependent and cannot merge
groups after they are created. A low-complexity improvement would be to run a
second pass that compares group centroids and merges sufficiently similar groups,
then repeats until no more merges are possible. Merges involving weak or mixed
groups should remain reviewable.

## Prototype decision

For the initial grouping prototype, use a k-nearest-neighbor similarity graph
with mutual-nearest-neighbor edges.

For each face:

1. Find its top `k` other faces by cosine similarity.
2. Add an edge only when the relationship is mutual: each face appears in the
   other's top-`k` neighbor list.
3. Apply the configured minimum similarity threshold to the edge.
4. Form provisional groups from the resulting graph's connected components.

The prototype should compare these `k` values:

- `k=5`
- `k=10`
- `k=20`
- `k=40`

The same embeddings, similarity threshold, dataset, and processing hardware
must be used for each value of `k`. The comparison should report accuracy,
runtime, memory, graph size, cluster count, and manual review effort. The
default `k` should be selected from those results.

The minimum edge-similarity threshold remains configurable. To isolate the
effect of `k`, the first comparison should hold that threshold constant using
the selected baseline from the existing grouping experiments. A separate
threshold sweep can be performed after a useful `k` range is identified.

## Possible future prototype progression

Evaluate these approaches in order:

1. Full pairwise similarity graph with connected components.
2. Mutual-nearest-neighbor graph.
3. Agglomerative clustering with average or complete linkage.
4. Optional DBSCAN or HDBSCAN comparison.

The first prototype should generate a browsable cluster review page and a
threshold sweep report. It should show cluster images, cluster sizes, and
metrics for each threshold or algorithm configuration.

The selected prototype is also the current preferred production direction. It
provides more control than raw connected components while remaining
understandable and compatible with the discovery-first workflow. More complex
alternatives should only be pursued if this prototype cannot meet accuracy,
scale, or review-effort requirements.

## Current provisional grouper

The current prototype uses an online greedy centroid algorithm:

1. Process faces in database order.
2. Compare each face with the centroid of every existing group.
3. Add it to the highest-scoring group if similarity exceeds the threshold.
4. Otherwise create a new group.
5. Update the selected group's centroid.

The current default threshold is `0.55`. This implementation is fast and useful
as a baseline, but it is not a final clustering algorithm. Its results depend
on processing order, it cannot merge groups later, and a centroid can become
less representative when mixed faces are added.

## Comparison criteria

All grouping methods should use the same embeddings, reviewed labels, dataset
splits, parameter grid, hardware, and timing procedure.

### Accuracy

Report:

- Cluster purity.
- Pair precision.
- Pair recall.
- F-score.
- Mixed-person cluster rate.
- Same-person fragmentation.
- Unknown/noise handling.
- Stability when photos are processed in a different order.

Discovery grouping should prioritize high pair precision. Accidentally combining
two people is generally worse than splitting one person into multiple groups.

### Speed

Measure:

- Embedding or index construction time.
- Grouping time.
- Time per face.
- Time to add new photos incrementally.
- Time to regroup after reference faces are added.
- Cold-start and warm-start performance.
- Total wall-clock time.

### Compute and storage

Measure:

- Peak RAM.
- CPU and accelerator utilization where available.
- Temporary disk usage.
- Persistent index size.
- Number of similarity comparisons.
- Whether processing can be paused and resumed.

### Manual review effort

Measure:

- Number of clusters.
- Number of clusters requiring review.
- Number of faces requiring individual review.
- Average and median cluster size.
- Number of mixed clusters.
- Number of duplicate clusters for the same person.
- Estimated clicks or assignments needed to name all people.
- Number of corrections after initial review.

A method with slightly lower mathematical accuracy may be preferable if it
substantially reduces review effort without creating dangerous mixed-person
clusters.

## Scale considerations

### Approximately 10,000 photos

Assuming two faces per photo, this represents roughly 20,000 face embeddings.
At this size:

- The current centroid grouper is inexpensive.
- A full pairwise similarity benchmark is possible with batching.
- A dense 20,000-by-20,000 float32 matrix would require about 1.6 GB before
  application overhead and should not be loaded unnecessarily.
- Sparse pairwise or k-nearest-neighbor graphs are preferable for normal use.
- Agglomerative clustering may be usable on subsets or sparse graphs.
- Exact pairwise results can provide an accuracy reference for faster methods.

Recommended evaluation sequence:

1. Benchmark the centroid grouper.
2. Build an exact or batched k-nearest-neighbor graph.
3. Compare ordinary and mutual-neighbor edges.
4. Optionally test HDBSCAN or agglomerative clustering on the sparse graph.

### Approximately 250,000 photos

Assuming two faces per photo, this represents roughly 500,000 face embeddings.
Full pairwise comparison is not practical:

- There would be approximately 125 billion face pairs.
- A dense float32 similarity matrix would require roughly 1 TB.
- Repeated full regrouping would be too slow and costly.
- A full agglomerative implementation would not be appropriate.

The large-collection design should use:

- An approximate nearest-neighbor index.
- A fixed number of neighbors per face.
- Sparse graph storage.
- Batch or incremental processing.
- Periodic graph maintenance rather than complete rebuilds.
- Separate handling for high-confidence and uncertain edges.

Recommended large-collection workflow:

1. Generate embeddings once.
2. Add embeddings to an approximate nearest-neighbor index.
3. Query the top `k` neighbors for each new face.
4. Create only sparse candidate edges.
5. Apply similarity and mutual-neighbor rules.
6. Update affected clusters incrementally.
7. Run occasional maintenance passes to identify possible merges or splits.

Approximate nearest-neighbor search may miss true relationships. Its recall
should be measured against exact nearest-neighbor results on representative
subsets.

## Recommended evaluation stages by collection size

For a 10,000-photo dataset:

1. Use the current centroid grouper as the baseline.
2. Run exact pairwise or exact k-nearest-neighbor calculations on a manageable
   subset.
3. Compare centroid grouping, connected components,
   mutual-nearest-neighbor graphs, agglomerative clustering, and optionally
   HDBSCAN.
4. Record accuracy, timing, RAM, cluster counts, and review effort.

For a 250,000-photo dataset:

1. Do not start with a dense pairwise graph.
2. Benchmark approximate nearest-neighbor index options and `k` values.
3. Compare incremental grouping strategies using sparse neighbor graphs.
4. Validate approximate-neighbor recall against exact searches on samples.
5. Measure incremental update cost and review workload.

The likely long-term design is a simple centroid grouper for immediate
provisional results, a sparse mutual-nearest-neighbor graph for higher-quality
grouping, incremental approximate-neighbor indexing for large collections, and
periodic merge/split maintenance as the collection grows.

## Dataset size for benchmarking

The existing reviewed dataset is useful for correctness and initial parameter
selection:

- 453 photos.
- 1,060 detected faces.
- 1,043 person-labeled faces.
- 57 labeled people.
- 20 people with reference faces.
- 37 people without reference faces.
- 17 unknown faces.

It is sufficient for verifying the mutual-kNN implementation, comparing
`k=5,10,20,40`, measuring grouping quality, and evaluating manual review
behavior. It is not sufficient for predicting large-library runtime, memory
growth, approximate-index behavior, or performance with many more identities.

The grouping workload is driven primarily by the number of faces rather than
the number of photos. Useful benchmark stages are:

| Stage | Faces | Approximate photos | Purpose |
|---|---:|---:|---|
| Existing dataset | 1,000 | 450 | Correctness and initial `k` comparison |
| Small scale | 10,000 | 4,000–8,000 | Initial performance and memory measurements |
| Medium scale | 50,000 | 20,000–40,000 | Index and graph behavior |
| Large scale | 100,000–250,000 | 40,000–200,000 | Large-library behavior |
| Stress scale | 500,000+ | 250,000+ | Long-term scalability testing |

The photo estimates assume roughly one or two faces per photo. A personal
library may have a different ratio.

A useful grouping benchmark should include at least 100–200 people, people with
both many and few photos, different poses and image quality, unknown people,
co-occurring people, near-lookalikes, and both reference-backed and
non-reference people.

The existing dataset should remain the accuracy and initial parameter-selection
dataset. Larger datasets should be added progressively. Synthetic expansion is
acceptable for runtime, memory, index construction, graph storage, and
incremental-update tests, but repeated embeddings should not be used to judge
grouping accuracy because they make clustering unrealistically easy.

## Transition from exact kNN search to ANN

The grouping method can remain a mutual-kNN graph while the nearest-neighbor
search implementation changes from exact search to approximate nearest-neighbor
(ANN) search.

Approximate guidance, assuming 512-dimensional float32 embeddings and an Apple
Silicon machine with 8 GB RAM:

| Faces | Recommendation |
|---:|---|
| Under 10,000 | Exact batched search |
| 10,000–50,000 | Exact search is practical for occasional rebuilds |
| 50,000–100,000 | Benchmark exact search against ANN |
| 100,000–250,000 | ANN is likely worthwhile |
| 250,000+ | ANN should be the default |

Exact all-pairs work grows quadratically:

- 10,000 faces: 100 million comparisons.
- 50,000 faces: 2.5 billion comparisons.
- 100,000 faces: 10 billion comparisons.
- 250,000 faces: 62.5 billion comparisons.

Batched computation avoids storing the full similarity matrix, but it does not
avoid the comparisons. Embedding storage is less problematic: 100,000
512-dimensional float32 embeddings require approximately 200 MB, and 500,000
require approximately 1 GB before application overhead.

Begin evaluating ANN when a full regrouping takes more than a few minutes, when
regrouping is needed after every import, when the collection reaches roughly
50,000–100,000 faces, or when exact search creates unacceptable CPU or memory
pressure. For a 250,000-photo library, assuming one or two faces per photo,
ANN should be part of the normal design.

The transition should proceed as follows:

1. Keep the mutual-kNN graph definition unchanged.
2. Implement exact batched neighbor search first.
3. Measure runtime and memory at 10,000, 50,000, and 100,000 faces.
4. Add ANN when exact search becomes inconvenient.
5. Compare ANN neighbors with exact neighbors on representative samples.
6. Track neighbor recall, cluster purity, pair precision, and pair recall.
7. Adopt ANN only if its runtime improvement justifies any lost neighbor
   relationships.

## Relationship to reference-based recognition

Grouping does not require reference faces. It can group faces using only their
embeddings. Once a user confirms a provisional cluster, selected confirmed
faces can be promoted to reference faces for that person.

Adding confirmed faces to the reference gallery can change later recognition
results by changing similarity scores, candidate rankings, and score margins.
Existing stored predictions must be recalculated after the reference gallery
changes. A mixed or uncertain cluster should not be promoted wholesale; only
faces that have been confirmed should become references.
