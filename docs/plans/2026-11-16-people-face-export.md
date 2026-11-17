# People and face gallery export

## Summary

Add `export people` for gallery-oriented collection, photo, and person exports.
Support columns (default), CSV, and hierarchical JSON output, optional output
files, inclusion/detail controls, and reproducible query metadata.

## Command and filtering

- `export people collection --collection-id ID [--collection-id ID ...]`
- `export people photo --collection-id ID --photo RELATIVE_PATH`
- `export people person --person-id ID`
- Default faces are named provisional/system/user assignments.
- `--include-unnamed` adds assigned people without names.
- `--include-unassigned` adds unknown, ambiguous, and unassigned faces.
- `--include-missing` includes paths marked missing; otherwise active paths only.
- `--scores`, `--with-ids`, `--pretty`, `--force`, and `--format` control output.

## Output

Base fields are always exported. Scores use the latest recognition candidate and
active cluster. JSON is a versioned envelope containing resolved query metadata,
counts, timestamp, and collections → photos → faces. Output files are protected
unless `--force` is supplied.

## Tests

Cover scope validation, filtering, counts, missing paths, duplicate assets,
score/cluster details, all formats, JSON pretty printing, empty photos,
deterministic ordering, and safe output replacement.
