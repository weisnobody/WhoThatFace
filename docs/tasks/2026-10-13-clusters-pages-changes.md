# Cluster Review Pages Alignment Task List

This task list compares the current `/review/people` and
`/review/people/<id>` pages with `/review/clusters` and
`/review/clusters/<id>`, then lists changes to make the cluster pages
consistent with the people review experience while preserving cluster-specific
behavior.


## Notes ##

- Clusters linked to a person
  * Clusters, typically by their nature, link several faces to a single person (proposed_person_id), though mixed clusters have faces are not all linked to the same person.
  * Indicate "Multiple" in the "Person" column on the /reviews/clusters page
  * List them in order of the number of faces with an assignment to the person in a comma sperated list below the Cluster header


## `/review/clusters` list page

### Page structure and layout

- [x] Add a people-style heading with the visible count and total count, for
  example `Clusters (10/47)`.
- [x] Give the controls form a dedicated ID and page-specific style rules.
- [x] Match the people page’s control spacing, wrapping, and responsive layout.
- [x] Put the sorting controls on a separate row below the primary filters.
- [x] Use the same Apply-button treatment as the people page.
- [x] Increase the space before Apply consistently with the people page.
- [x] Preserve all filter, sort, direction, and pagination parameters when
  generating pager links and page-jump forms.

### Filters

- [x] Preserve the existing Mixed filter.
  - [x] Any.
  - [x] Mixed.
  - [x] Pure.
- [x] Preserve the existing Status filter.
  - [x] Any status.
  - [x] Unreviewed.
  - [x] In progress.
  - [x] Partially reviewed.
  - [x] Reviewed.
  - [x] Needs review.
  - [x] Superseded, if historical clusters are exposed.
- [x] Preserve the existing Rating filter.
  - [x] Any.
  - [x] High.
  - [x] Medium.
  - [x] Low.
- [x] Preserve the Include Don’t Track checkbox.
- [x] Preserve minimum-face and maximum-face numeric filters.
- [x] Add a minimum and maximum consistency score filter
- [x] Add a run/status filter so historical and superseded clusters can be viewed.
  * Default is to only show the active cluster
- [x] Add cluster/person search text field to search cluster id, person id, person name
- [x] Render all submitted filter values as selected or populated when the
  page reloads.
- [x] Use human-readable labels instead of raw database values where useful.

### Sorting

- [x] Keep sorting by Mixed.
- [x] Keep sorting by Face count.
- [x] Keep sorting by Candidate count.
- [x] Keep sorting by Person.
- [x] Keep sorting by Rating.
- [x] Add sorting by Review status
  * Use a secondary sort on the number of faces that are not assigned-user / assigned-system
- [x] Add sorting by "To Be Assigned" (the number of faces that are not assigned-user / assigned-system)
- [x] Add sorting by Cluster key.
- [x] Render the selected sort option after submission.
- [x] Render the selected ascending/descending direction after submission.
- [x] Use a stable secondary sort, such as cluster key and cluster ID.
- [x] Add sort direction pulldown

### Pagination

- [x] Add per-page selection.
- [x] Add First, Previous, page number, Next, and Last links.
- [x] Add a page-jump input.
- [x] Clamp invalid or out-of-range page values consistently with the people
  page.
- [x] Preserve active filters and sort options in every pagination URL.

### Table presentation

- [x] Add a dedicated table class matching the people page’s table styling.
- [x] Use consistent cell padding, borders, width behavior, and whitespace.
- [x] Keep the Cluster link as the primary row link.
- [x] Keep the Proposed Person link to `/review/people/<id>`.
  * [x] Add the representative image before the person's name (like in the people list)
  * [x] Use the person's representative image if available
  * [x] If not use the the highest-quality face for the primary person.
- [x] Display an explicit fallback such as `Unassigned` when no proposed person
  exists.
- [x] Keep Face count.
- [x] Keep Candidate count.
  * [x] Ue the count the faces with a score of 0.015 or higher
  * [x] If all faces are assigned-user or assigned-system (which means there are no candidates), put N/A rather than 0
- [x] Keep Purity.
- [x] Keep Connectivity.
- [x] Keep Mixed status.
  * [x] Show an "X" if it's true, otherwise leave the cell empty.
- [x] Keep Review status, change column header to "Review"
- [x] Add a "Consistency" column before Purity
- [x] Add a "To Assign" column after candidates that is the number of faces in the cluster that are not assigned-user / assigned-system
- [x] Add an "Assigned Match" column after "To Assign" that shows the number of assigned-user / assigned-system faces that are the same person as the clusters proposed_person_id
  * [x] If it's a mixed cluster, show a "-".
  * [x] If there's no "assigned" faces leave the cell empty
- [x] After "Status" add a "Type" column to show if it's the active, superseded or historical

### Access and data scope

- [x] Keep collection-permission filtering consistent with the existing page.
- [x] Photos / Faces
  * If user doesn't have access to any faces in a cluster, the cluster should not show up in the list
  * Counts on the cluster list page should not include faces on photos that the user does not have access to
  * On cluster detail page, faces on photos that the user does not have access to should not be shown and are not included in group actions
- [ ] representative-images are accessable by any user even if they don't have access to the photo the face comes from
- [x] Ensure counts and candidate totals use the same accessible face scope as
  the rows being displayed.
- [x] Make active-versus-historical run behavior visible if both are exposed.


## `/review/clusters/<id>` detail page


### Header and summary

- [x] Add a people-style page header with a readable cluster title and cluster
  key/ID.
- [x] Link the proposed person to `/review/people/<id>`.
- [x] Show a summary containing:
  - [x] Face count.
  - [x] Candidate count.
    * [x] If all faces are assigned-user or assigned-system (which means there are no candidates), put N/A rather than 0
  - [x] Proposed person.
  - [x] Consistency
  - [x] Purity.
  - [x] Connectivity.
  - [x] Mixed status.
  - [x] Review status.
  - [x] Clustering-run status.
- [x] Keep the review version visible where it is useful for concurrency
  handling.
- [x] Add a clear link back to `/review/clusters`.

### Status and warnings

- [x] Move mixed-cluster warnings into a consistent alert area near the header.
- [x] Move historical/superseded-run warnings into the same alert treatment.
- [x] Keep the special mixed-cluster behavior:
  - [x] Disable bulk default-person assignment.
  - [x] Require explicit per-face decisions.
- [x] Make historical-cluster read-only behavior visually obvious.
- [x] Distinguish review status from clustering-run status.
- [x] Rathern than "run: <status>", show "Cluster: <status>"

### Styles and layout

- [x] Styles and layout should be consistent with /review/people/<id>.  Use same css style names for both
- [x] Move the inline cluster `<style>` block into the page’s `PageStyle`.
- [x] Establish shared style conventions with the people detail page.
- [x] Use a consistent responsive grid and card width strategy
- [x] Use shared spacing, typography, border, and control styles.
- [x] Keep cluster-specific styling for mixed state and assignment state.
- [x] Ensure the page remains usable on narrow screens.

### Review controls

- [x] Keep the Default person control.
  * Have it preselect the person if there's only one person represented in the cluster
  * If multiple people are represented, leave it as "No default", but put the people represented at the top of the list
  * Adjust so it matches the "Bulk Assignment" form on the /review/people/<id> page.
- [x] Keep the review Status control.
- [x] Keep the Note field.
- [x] Keep the Save review action.
- [x] Place these controls in a prominent page-action area comparable to the
  people detail page.
- [x] Keep Save behavior and optimistic/concurrency version handling unchanged
  unless a separate design decision is made.
- [x] Make the save result/error message visually consistent with the rest of
  the page.

### Detail filters and sorting

- [x] Add filtering capabilities
  - [x] Support filtering by assignment state.
  - [x] Support filtering by assigned person.
  - [x] Support filtering by disposition.
  - [x] Support filtering by candidate presence.
  - [x] Support filtering by possible-candidate presence.
  - [x] Support quality and detection thresholds.
- [x] Add sorting by:
  - [x] Face/path.
  - [x] Person.
  - [x] Assignment state.
  - [x] Disposition.
  - [x] Consistency
  - [x] Quality.
  - [x] Detection.
  - [x] Candidate confidence.
- [x] Use the same Apply button and control layout as the person detail page.
- [x] Preserve filter and sort values in the rendered form.

### Face-card content

- [x] Keep the photo and face-box overlay.
- [x] Keep the photo-path link to `/review/photos`.
- [x] Keep the face identifier.
- [x] Display the current assignment/person prominently.
- [x] Display the current assignment state.
- [x] Display consistency, quality and detection scores using the people-page metadata
  conventions.
- [x] Display recognition candidates with links to person pages.
- [x] Display possible candidates when available, using the people-page
  possible-candidate presentation.
- [x] Keep the face disposition control.
- [x] Keep the person assignment control.
- [x] Keep the face-state control.
- [x] Use consistent assignment-state card border colors.
- [x] Use consistent card action placement, spacing, actions and input
- [x] Add an explicit empty-state message when no faces match active detail
  filters.
- [x] Distinguish “cluster has no faces” from “no faces match the filters.”

### Data and links

- [x] Link each face’s assigned person to its person detail page.
- [x] Link each face’s containing photo to the photo review page.
- [x] Link the cluster key/header to any relevant run or cluster context if
  useful.
- [ ] Show proposed-person and current face-assignment differences clearly when
  they disagree.
- [ ] Preserve candidate ranking, similarity, margin, support, and evidence
  information where available.

## Shared implementation work

- [x] Identify reusable helpers for page controls, pagination, selected options,
  and filter preservation.
- [x] Identify reusable CSS classes for review tables, controls, cards, alerts,
  metadata, and empty states.
- [x] Use existing functions for people-specific actions on cluster pages.
- [x] Keep cluster review mutations protected by the existing account and CSRF
  checks.
- [x] Preserve collection access checks for both list and detail pages.
- [x] Preserve historical-cluster safeguards.
- [x] Keep original photos and other private derived data out of generated
  fixtures and committed test assets.

## Tests

### Cluster list tests

- [ ] Verify the cluster page renders its heading and count.
- [ ] Verify all filters render.
- [ ] Verify submitted filters remain selected/populated.
- [ ] Verify the Apply button label and layout-break behavior.
- [ ] Verify every sort option renders.
- [ ] Verify selected sort and direction are preserved.
- [ ] Verify ascending and descending sorting for each supported sort.
- [ ] Verify stable tie-breaking.
- [ ] Verify minimum and maximum face filters.
- [ ] Verify mixed/pure filtering.
- [ ] Verify review-status filtering.
- [ ] Verify rating filtering.
- [ ] Verify Don’t Track filtering.
- [ ] Verify collection permissions.
- [ ] Verify pagination and page-jump links preserve all parameters.
- [ ] Verify preview selection, if previews are added.

### Cluster detail tests

- [ ] Verify the header summary fields.
- [ ] Verify proposed-person and photo/person links.
- [ ] Verify mixed-cluster warnings and disabled default assignment.
- [ ] Verify historical-run warnings and read-only behavior.
- [ ] Verify cluster controls render with the correct current values.
- [ ] Verify face cards contain required metadata and actions.
- [ ] Verify candidate and possible-candidate rendering.
- [ ] Verify detail filters and sorting, if added.
- [ ] Verify empty-state behavior.
- [ ] Verify collection access enforcement.
- [ ] Verify viewer/editor action differences.
- [ ] Verify Save review behavior remains protected and version-aware.

## Suggested implementation order

- [x] Finalize the decisions at the top of this document.
- [x] Align the cluster list form, styles, selected values, and Apply behavior.
- [x] Add cluster-list count, pagination, and stable sorting.
- [x] Align the cluster table presentation and optional preview image.
- [x] Align the cluster detail header, alerts, and page styles.
- [x] Align cluster face-card content and links.
- [x] Add optional cluster-detail filtering and sorting.
- [x] Add or update regression tests after each group of changes.
- [x] Run the full Go test suite and `git diff --check`.
