# Correct and Complete Missed Photo Review Changes

## Summary

Close the gaps found after the initial photo-page alignment implementation. The
work covers detail-card behavior, access-scoped collection summaries,
representative-image authorization, database-backed pagination, broader person
matching/sorting, default-filter alignment, and regression coverage.

## Implementation changes

### Detail-page behavior

- For user/system-assigned faces, render only the face number, linked person,
  and assignment label. Do not render recognition candidates, possible
  candidates, cluster metadata, or mutation controls for these faces.
- Keep candidate lists, cluster/`No cluster` metadata, and remaining review
  controls only for unresolved/provisional faces.
- Make the detail-page photo-size query parameter affect the displayed photo
  dimensions using the same scale values and responsive rules as the list
  page.
- Preserve the required candidate → cluster → actions → assignment ordering
  for actionable faces.

### Permission-scoped collection summaries

- Pass the viewer’s accessible collection IDs into detail-page collection
  lookup.
- Show only accessible collections in the summary.
- Keep the detail page authorized by the collection represented by the
  requested `path_id`; do not let another path for the same asset broaden
  photo or collection access.
- Add tests proving that an asset shared by allowed and restricted collections
  exposes only the allowed collection links and names.

### Representative-image authorization

- Do not make all person IDs globally readable merely because the requester is
  authenticated.
- Permit a representative-image read when the person is related to at least
  one face in a collection accessible to the requester through any of these
  relationships:
  - current face assignment;
  - active cluster proposed person;
  - recognition candidate;
  - current possible candidate.
- Keep the existing modify permission checks for selecting or clearing a
  representative image.
- Ensure this authorization allows a candidate person’s image to display even
  when the person’s own assigned faces come only from inaccessible collections.
- Add tests for allowed candidate access, denied unrelated-person access,
  viewer/editor differences, and no leakage of original source photos.

### Database-backed photo review query

- Extend the review-photo query contract to return the fields required by the
  list without issuing one face query per photo.
- Apply filters, aggregate counts, sorting, and `LIMIT/OFFSET` in SQL before
  rendering the page.
- Keep deterministic ordering by the selected key followed by path/photo ID.
- Preserve accessible collection restrictions in every aggregate and
  subquery.
- Keep the page count query separate from the page-row query so the heading
  reports visible-page count and filtered total count without loading the
  entire result set into memory.
- Add indexes or query adjustments only where needed after verifying the
  generated SQL against the current SQLite schema.

### Person matching and sorting

- Extend person filtering to include:
  - current assigned person;
  - active cluster proposed person;
  - recognition candidates;
  - current possible candidates.
- Apply the same accessible-photo/face scope to every relationship.
- Define person sorting as the lowest normalized displayed person name/ID
  among all person relationships rendered or matched for the photo, with the
  photo ID as the stable secondary key.
- Add fixtures where a person appears only as a proposed or candidate person
  and verify both filtering and sorting.

### Default controls and semantics

- Align photo-list defaults with the people-page controls:
  - assignment categories selected by default;
  - status/include behavior matching the people page;
  - explicit all/with-faces/without-faces behavior;
  - OR semantics when multiple assignment or status options are selected.
- Decide and document the no-face default consistently between the plan and
  implementation. Keep the explicit `without faces` option available.
- Preserve all normalized values in the form and every pager URL.

## Tests

- Add search tests for:
  - each filter independently;
  - OR combinations of assignments and statuses;
  - proposed/candidate/possible person matching;
  - accessible versus restricted collection scope;
  - all sort directions and stable ties;
  - assigned percentage and to-be-assigned counts;
  - SQL page boundaries and total counts.
- Add web tests for:
  - assigned detail cards containing no candidates, cluster, or actions;
  - unresolved detail cards containing candidates, cluster fallback, actions,
    and assignment controls in the required order;
  - working detail photo-size variants;
  - restricted collection summaries;
  - representative-image authorization through candidate relationships;
  - unrelated-person representative-image denial;
  - viewer/editor behavior;
  - pagination/filter preservation and default control state.
- Run the focused search and web tests, the full Go test suite, and
  `git diff --check`.

## Acceptance criteria

- Assigned faces are informational only on the photo detail page.
- Actionable faces retain the complete intended review workflow.
- A user can see the name and representative image of a person who is a
  candidate for a face in an accessible photo, even if that person’s other
  faces are inaccessible.
- The same user cannot retrieve an unrelated person’s representative image by
  guessing a person ID.
- Collection summaries never reveal restricted collection names.
- Photo pagination does not load and sort the entire matching dataset in Go.
- Proposed and candidate people participate consistently in filtering and
  person sorting.
- The task checklist and tests accurately reflect the delivered behavior.

## V2 resolution status

The v2 follow-up resolved the remaining behavior details:

- Actionable faces expose the incorrect-assignment action while Reference and
  Don’t Track remain removed.
- Assigned-face cards omit candidates, cluster metadata, and actions.
- List-card person numbering uses persisted face indices.
- Candidate-only automatic representatives are supported with deterministic
  priority and ordering.
- Representative reads remain limited to people related to visible faces in
  accessible collections.
- The web list defaults to completed-face photos; `faces=all` and
  `faces=without` remain explicit modes.
- Don’t Track is excluded by default and only appears when explicitly
  selected.
