# Performance and evaluation protocol

Run the repeatable synthetic benchmark from the repository root:

```sh
go run ./cmd/face-me-bro-benchmark --output performance.json
```

The default populations are 10,000, 50,000, 100,000, and 250,000 faces.
Each report records host/runtime information and measurements for first scan,
unchanged rescan, duplicate-heavy scanning, analysis, recognition, exact and
ANN grouping, database writes and size, review query latency, incremental
updates, and adaptive throttling. Exact grouping is capped by
`--exact-max-faces` so a large run cannot accidentally start an impractical
all-pairs calculation. Use `--worker-path worker/insightface_worker.py` to
include worker lifecycle timing; model loading must already be configured for
the selected Python environment.

The ANN measurement is a dependency-free sign-projection prototype. It reports
recall against exact neighbors on the same sampled queries and is evaluation
tooling, not a final production index. Compare runtime, memory/database size,
neighbor recall, grouping quality, and review burden before changing a global
or collection-specific search method.

The exact/ANN setting is configured globally in `config.json` with
`neighbor_search_method: "exact"` or `"ann"`. A collection can override it
with `collection set-search --collection-id ID --method exact|ann`.
