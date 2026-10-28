# Photos Review Pages Alignment Task List

These tasks align `/review/photos` and `/review/photos?path_id=<id>` with the
people and clusters review pages.

## Current status

The photo-page alignment work is implemented. Rating sorting and
user/collection-specific display identity are future work and are not part of
this task.

## `/review/photos` list page

### Page structure and filters

- [x] Add a people-style heading with visible and total counts, such as
  `Photos (10/47)`.
- [x] Add a dedicated `photos-controls` form with people-page spacing,
  wrapping, responsive behavior, and Apply behavior.
- [x] Add path/file-name and accessible-collection filters.
- [x] Add all/with-faces/without-faces filtering.
- [x] Add people-style OR assignment controls for User, System, Provisional,
  and Other faces.
- [x] Add OR status controls for Don't Track, Unknown, and Possibles.
- [x] Add person text matching across assigned people, proposed people,
  recognition candidates, and possible candidates.
- [x] Preserve submitted filter values and use human-readable labels.
- [x] Add query-parameter-backed photo-size controls for 1/4, 1/2, 1, 1.5,
  and 2, defaulting to 1.
- [x] Default the web list to completed-face photos; preserve explicit All and
  Without Faces modes.
- [x] Exclude Don't Track faces by default while keeping Unknown and Possibles
  visible; explicit status selections use OR semantics.

### Sorting and pagination

- [x] Sort by face count, person, assigned percentage, and To Be Assigned.
- [x] Preserve selected sort and direction.
- [x] Use photo ID as a stable secondary sort.
- [x] Add per-page selection, First/Previous/page/Next/Last links, and a
  page-jump input.
- [x] Clamp invalid or out-of-range pages consistently with `/review/people`.
- [x] Preserve filters, sort, direction, and photo size in pagination URLs.
- Rating sorting is deferred because no photo-level rating source is defined.

### Card presentation and access

- [x] Render responsive photo cards with people/cluster-style spacing, borders,
  widths, and whitespace.
- [x] Show `Faces: <needs review> / <total>` and numbered linked people.
- [x] Show `Unassigned` without a link when no person is linked.
- [x] Use `System`, `User`, and `Prov` assignment labels.
- [x] Number listed people using each face's persisted face index.
- [x] Apply green, yellow, and red card states based on face assignments.
- [x] Preserve collection permission filtering and keep all rendered face
  aggregates within the accessible photo scope.
- [x] Keep aggregate counts aligned with the returned face scope.
- [x] Allow authenticated representative-image reads without requiring access
  to the source photo; original-photo access remains collection-protected.

## `/review/photos?path_id=<id>` detail page

### Header, summary, and layout

- [x] Replace “Photo review” with the relative file path.
- [x] Add a people-style summary for assigned/total faces, linked people, and
  collection links back to the filtered photo list.
- [x] Align spacing, typography, borders, responsive grid, and controls with
  the people and cluster detail pages.
- [x] Add the shared query-parameter-backed photo-size controls.
- [x] Show the photo review count and add face numbers to bounding boxes.

### Face cards

- [x] Show `Unassigned` when no person is linked.
- [x] Remove Reference and Don't Track controls.
- [x] For user/system-assigned faces, show only the face number, person, and
  assignment type; omit review forms and actions.
- [x] For other faces, reuse cluster-detail conventions for candidates,
  possible candidates, cluster context, review actions, and assignment.
- [x] Include the Incorrect Assignment action for actionable faces while
  keeping Reference and Don't Track removed.
- [x] Show explicit `No cluster` when no active cluster is available.
- [x] Link displayed people and available cluster context.
- [x] Render every face in an accessible photo.

## Shared implementation and tests

- [x] Reuse people/cluster conventions for controls, pagination, selected
  values, filter preservation, cards, metadata, and empty states.
- [x] Preserve account and CSRF protection for existing mutations.
- [x] Preserve collection access checks for original photos and detail pages.
- [x] Keep private photos, embeddings, databases, and generated data out of
  committed fixtures.
- [x] Add search tests for filters, OR behavior, person matching, face scope,
  aggregates, no-face behavior, sorting, and stable ties.
- [x] Add web tests for list controls, selected values, photo size, card
  output, pagination state, detail summaries, links, face actions, candidate
  and cluster rendering, and permissions.
- [x] Add representative-selection tests for candidate-only people and deny
  unrelated person IDs.
- [x] Run the full Go test suite and `git diff --check`.

## Future work

- Define and implement photo-level Rating and rating sorting.
- Define user- and collection-specific display names and representative images
  in the design/specification review document; this is not part of the photo
  page alignment work.
