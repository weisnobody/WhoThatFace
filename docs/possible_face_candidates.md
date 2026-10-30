# Possible Face Candidates

## Purpose

This feature finds likely person matches for faces that do not currently have a
strong identity assignment. It is intended to connect small provisional people
and unknown faces with established people who have approved reference faces.

The feature provides review candidates only. It must not silently change a
user-confirmed assignment, merge people, or turn a low-confidence match into a
system assignment.

## Use cases

The feature supports these cases:

1. A provisional person contains one face (a singleton provisional person).
2. A named provisional person contains a small number of faces, with five
   faces as the default maximum.
3. An unnamed, system-created temporary person contains up to 50 faces by
   default.
3. A face is `unknown`, with no person assignment.
4. A face is `ambiguous`, because the user could not identify it.
5. A face is `unassigned`, where processing has not produced an acceptable
   person assignment.

`assigned-system` faces are excluded. Once a face reaches any `assigned-*`
state, the user must explicitly change it before it becomes eligible.

## Terminology

- A *target face* is a face being evaluated for possible alternative people.
- A *target provisional person* is a provisional person associated with one to
  five currently provisional faces, or an unnamed temporary person associated
  with one to 50 faces by default.
- A *candidate person* is an established person with at least one approved
  reference face.
- A *possible candidate* is a candidate person that has been scored for a target
  face but did not necessarily meet the threshold for an automatic assignment.
- An *approved reference face* is a face explicitly selected by the user with
  `reference = true`. Recommended reference faces are not used as matching
  examples.

## Eligibility

### Default target set

The default run includes faces satisfying at least one of these conditions:

- The current assignment state is `unknown`, `ambiguous`, or `unassigned`.
- The face has a `provisional` assignment and its assigned person is
  provisional, with a total current provisional face count within the
  applicable limit: five for named provisional people and 50 for unnamed
  temporary people.

The default run excludes:

- `assigned-user` and `assigned-system` faces.
- Faces marked `dont-track`, unless an explicit include-don't-track option is
  supplied.
- Reference faces belonging to the candidate person being evaluated.
- Faces without a compatible embedding or pipeline provenance.

The implementation must evaluate the eligibility of each face at run time and
record the eligibility reason. A later assignment change must not alter the
historical result of an earlier run.

### Configurable limits

The following values are configuration parameters:

- Maximum provisional faces per target person: default `5`.
- Maximum faces per unnamed temporary person: default `50`.
- Maximum candidates retained per face: default `5`.
- Minimum candidate similarity retained: default `0.25`, matching the low
  similarity boundary in the recognition settings.
- Whether don't-track faces are included: default `false`.
- Whether candidates are generated for one person, one collection, or all
  accessible collections.

## Candidate generation

For each eligible target face, compare its embedding with the approved
reference gallery for every candidate person that has compatible references.
The initial implementation uses the existing exact cosine-similarity matcher.
An ANN-backed implementation may replace the search mechanism later without
changing the persisted candidate contract.

For each candidate person, calculate and retain:

- strongest face-to-reference similarity;
- second-best candidate similarity and margin, when available;
- number of approved reference faces supporting the person;
- target face quality and detection confidence;
- pipeline/model identity and reference-set version;
- evidence category calculated using the configured thresholds;
- rank among candidates for the target face.

The candidate score is evidence for review, not an assignment decision. A
candidate below the normal assignment threshold may still be retained if it is
within the configured similarity floor or is one of the top five candidates.
The result should indicate why it was retained, such as `above-floor` or
`top-ranked-below-threshold`.

Candidate people must be ranked deterministically by:

1. similarity descending;
2. margin descending;
3. support count descending;
4. person ID ascending.

The system should retain the top five candidates per target face by default,
including candidates that do not meet medium evidence. It should not assign a
person merely because the person is the highest-ranked candidate.

## Persistence

Possible candidates must be persisted separately from the current assignment
and from ordinary automatic recognition candidates, because they are an
alternative-review result and may be below assignment thresholds.

The data model should contain a run record and candidate rows.

### Possible-candidate run

Each run stores:

- UUIDv7 run ID;
- creation, start, and completion timestamps;
- requested scope: person, collection, or all;
- target eligibility policy and configuration snapshot;
- pipeline/model ID and provenance;
- reference-set version or reference snapshot timestamp;
- status and error summary;
- target-face count and candidate-row count.

### Possible-candidate row

Each row stores:

- UUIDv7 row ID;
- run ID;
- target face ID;
- candidate person ID;
- rank;
- similarity and margin;
- support count;
- quality and detection values;
- evidence category;
- retention reason;
- whether the row is current for that target face;
- creation timestamp.

Rows from older runs must remain available for audit and comparison. A new run
marks prior rows for the same target face as non-current; it does not delete
them. Candidates must be invalidated when the referenced person or face is
removed, but the run history should remain readable.

## Interaction with assignments and clustering

Possible candidates do not change assignment state. In particular:

- `unknown` remains `unknown` until the user assigns it or a separate normal
  recognition process produces an allowed automatic assignment.
- `ambiguous` remains `ambiguous` until the user resolves it.
- A provisional assignment remains provisional unless the user confirms it or
  normal evidence processing changes it according to the assignment policy.
- `assigned-user` is never changed by this feature.
- `assigned-system` is not changed by default; an explicit comparison mode may
  display alternatives.

Clustering results may be shown beside possible candidates, including the
current cluster, proposed person, purity, connectivity, consistency, quality,
and detection. Clustering is supporting evidence only. A possible candidate
must not be treated as a cluster merge, and candidate generation must not
modify cluster membership.

If the user assigns a target face to a candidate person, the assignment is
recorded as `assigned-user` and the normal audit and reprocessing rules apply.
The user may instead reject the candidate, leave the face unchanged, mark it
`unknown` or `ambiguous`, or assign it to a different person.

## Scheduling and commands

Candidate discovery may be started:

- manually for one person;
- manually for a collection;
- manually for all accessible collections;
- after approved references change, subject to the existing 60-second
  reference-reprocess debounce;
- periodically as a maintenance job.

The job must coalesce duplicate pending requests for the same scope and
reference-set version. A run should be safe to cancel and retry. It must not
start a full clustering run as a side effect.

The CLI should eventually provide commands equivalent to:

```text
whothatface job possible-candidates --person-id PERSON_ID
whothatface job possible-candidates --collection-id COLLECTION_ID
whothatface job possible-candidates --all
whothatface face candidates --face-id FACE_ID
```

The command output should identify the job or run, target scope, number of
eligible faces, and number of candidate rows retained.

## Review web interface

The review UI should expose possible candidates in these places:

- On a face card, show the top candidates in descending rank order with
  similarity, margin, support, quality, detection, and evidence level.
- On a provisional-person page, provide a filter for faces with possible
  candidates and show the candidate person names and scores.
- On an unknown/ambiguous review page, provide the same candidate display.
- Provide a link to the candidate person and to the candidate run details.
- Show when the candidates were generated and whether they are stale because
  references or the pipeline changed.

The default action for a candidate is review, not acceptance. The UI should
make clear that “highest candidate” means the best available match and does not
mean that the system is confident the candidate is correct.

## Safety and privacy

- Candidate generation must not overwrite user assignments.
- Don't-track faces remain excluded by default, while preserving the explicit
  option to inspect them.
- Embeddings and candidate details are local application data and must not be
  included in logs or exported reports unless explicitly requested.
- Every user assignment made from a possible candidate must create the normal
  audit event, including the prior assignment state and selected candidate.

## Acceptance criteria

The feature is complete when:

1. Singleton provisional, small provisional, unknown, ambiguous, and
   unassigned faces can be selected by the documented eligibility rules.
2. Each eligible face can receive up to five persisted possible candidates
   from people with approved references.
3. Candidate generation does not modify assignments or clusters.
4. Repeated requests are coalesced and use the current reference-set version.
5. Candidate scores and provenance are visible in the CLI and review UI.
6. User assignment from a candidate produces the normal `assigned-user` state
   and audit record.
7. User-confirmed and don't-track exclusions behave as documented.
8. Tests cover eligibility, ranking, persistence, stale runs, coalescing, and
   protection of existing assignments.
