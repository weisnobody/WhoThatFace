# Add configurable direct assignment during recognition

## Summary

Add a configurable similarity-only rule that assigns a face as
`assigned-system` when its highest-ranked known-person match exceeds `0.75`,
without requiring cluster membership or proposed-person agreement.

The rule will apply to ordinary recognition and reference-reprocess runs.
Existing `assigned-user` and `assigned-system` faces will never be
automatically changed.

## Implementation changes

- Add `DirectAssignmentSimilarity` to recognition thresholds.
  - Default: `0.75`.
  - Config key: `recognition_direct_assignment_similarity`.
  - Load the value from the config file and expose it in the example
    configuration.
- Update recognition processing:
  - Exclude current `assigned-user` and `assigned-system` faces from
    automatic processing.
  - For each eligible face, use only the highest-ranked recognition
    candidate.
  - If its similarity is strictly greater than
    `DirectAssignmentSimilarity`, assign it to that person as
    `assigned-system`.
  - Record normal similarity, margin, support, quality, and detection
    evidence, with a distinct reason such as `direct-similarity`.
  - If the direct threshold is not met, preserve the existing evidence-based
    assignment behavior.
- Update reference-reprocess:
  - Replace the current person-linked `ProcessPerson` scope with a broad scan
    using the selected person’s approved references.
  - Evaluate unrelated faces across the database, not only faces already
    linked to that person or its existing candidates.
  - Apply the direct-assignment rule without requiring cluster membership or
    proposed-person agreement.
  - Do not start clustering as a side effect of reference-reprocess.
- Keep `assigned-user` and `assigned-system` faces protected from both normal
  recognition and reference-reprocess.
- Preserve existing possible-candidate behavior; faces that become
  `assigned-system` will naturally no longer be eligible for possible-
  candidate discovery.

## Tests

Add coverage for:

- Default threshold is `0.75`.
- Config override changes the threshold.
- An unrelated unknown, ambiguous, provisional, or unassigned face above the
  threshold becomes `assigned-system`.
- The assignment works even when there is no cluster or the cluster’s
  proposed person differs.
- A score equal to `0.75` triggers the rule; the score must be
  greater than or equal. 
- The highest-ranked candidate is selected when multiple candidates exceed
  the threshold.
- If the threshold is set equal to or lower than recognition_high_similarity, throw an error and exit.
- Existing `assigned-user` faces remain unchanged.
- Existing `assigned-system` faces remain unchanged, including when they
  currently belong to another person.
- Reference-reprocess scans unrelated faces using the selected person’s
  references.
- Scores below the direct threshold continue through the existing recognition
  classification path.
- Assignment events retain the expected evidence and audit reason.

No database migration is required; this uses the existing assignment-event
and recognition-candidate structures.
