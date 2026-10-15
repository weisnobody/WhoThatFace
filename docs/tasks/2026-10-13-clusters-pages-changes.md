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

- [ ] Add a people-style heading with the visible count and total count, for
  example `Clusters (10/47)`.
- [ ] Give the controls form a dedicated ID and page-specific style rules.
- [ ] Match the people page’s control spacing, wrapping, and responsive layout.
- [ ] Put the sorting controls on a separate row below the primary filters.
- [ ] Use the same Apply-button treatment as the people page.
- [ ] Increase the space before Apply consistently with the people page.
- [ ] Preserve all filter, sort, direction, and pagination parameters when
  generating pager links and page-jump forms.

### Filters

- [ ] Preserve the existing Mixed filter.
  - [ ] Any.
  - [ ] Mixed.
  - [ ] Pure.
- [ ] Preserve the existing Status filter.
  - [ ] Any status.
  - [ ] Unreviewed.
  - [ ] In progress.
  - [ ] Partially reviewed.
  - [ ] Reviewed.
  - [ ] Needs review.
  - [ ] Superseded, if historical clusters are exposed.
- [ ] Preserve the existing Rating filter.
  - [ ] Any.
  - [ ] High.
  - [ ] Medium.
  - [ ] Low.
- [ ] Preserve the Include Don’t Track checkbox.
- [ ] Preserve minimum-face and maximum-face numeric filters.
- [ ] Add a minimum and maximum consistency score filter
- [ ] Add a run/status filter so historical and superseded clusters can be viewed.
  * Default is to only show the active cluster
- [ ] Add cluster/person search text field to search cluster id, person id, person name
- [ ] Render all submitted filter values as selected or populated when the
  page reloads.
- [ ] Use human-readable labels instead of raw database values where useful.

### Sorting

- [ ] Keep sorting by Mixed.
- [ ] Keep sorting by Face count.
- [ ] Keep sorting by Candidate count.
- [ ] Keep sorting by Person.
- [ ] Keep sorting by Rating.
- [ ] Add sorting by Review status
  * Use a secondary sort on the number of faces that are not assigned-user / assigned-system
- [ ] Add sorting by "To Be Assigned" (the number of faces that are not assigned-user / assigned-system)
- [ ] Add sorting by Cluster key.
- [ ] Render the selected sort option after submission.
- [ ] Render the selected ascending/descending direction after submission.
- [ ] Use a stable secondary sort, such as cluster key and cluster ID.
- [ ] Add sort direction pulldown

### Pagination

- [ ] Add per-page selection.
- [ ] Add First, Previous, page number, Next, and Last links.
- [ ] Add a page-jump input.
- [ ] Clamp invalid or out-of-range page values consistently with the people
  page.
- [ ] Preserve active filters and sort options in every pagination URL.

### Table presentation

- [ ] Add a dedicated table class matching the people page’s table styling.
- [ ] Use consistent cell padding, borders, width behavior, and whitespace.
- [ ] Keep the Cluster link as the primary row link.
- [ ] Keep the Proposed Person link to `/review/people/<id>`.
  * Add the representative image before the person's name (like in the people list)
  * Use the person's representative image if available
  * If not use the the highest-quality face for the primary person.
- [ ] Display an explicit fallback such as `Unassigned` when no proposed person
  exists.
- [ ] Keep Face count.
- [ ] Keep Candidate count.
  * Ue the count the faces with a score of 0.015 or higher
  * If all faces are assigned-user or assigned-system (which means there are no candidates), put N/A rather than 0
- [ ] Keep Purity.
- [ ] Keep Connectivity.
- [ ] Keep Mixed status.
  * Show an "X" if it's true, otherwise leave the cell empty.
- [ ] Keep Review status, change column header to "Review"
- [ ] Add a "Consistency" column before Purity
- [ ] Add a "To Assign" column after candidates that is the number of faces in the cluster that are not assigned-user / assigned-system
- [ ] Add an "Assigned Match" column after "To Assign" that shows the number of assigned-user / assigned-system faces that are the same person as the clusters proposed_person_id 
  * If it's a mixed cluster, show a "-".
  * If there's no "assigned" faces leave the cell empty
- [ ] After "Status" add a "Type" column to show if it's the active, superseded or historical

### Access and data scope

- [ ] Keep collection-permission filtering consistent with the existing page.
- [ ] Photos / Faces 
  * If user doesn't have access to any faces in a cluster, the cluster should not show up in the list
  * Counts on the cluster list page should not include faces on photos that the user does not have access to
  * On cluster detail page, faces on photos that the user does not have access to should not be shown and are not included in group actions
- [ ] representative-images are accessable by any user even if they don't have access to the photo the face comes from
- [ ] Ensure counts and candidate totals use the same accessible face scope as
  the rows being displayed.
- [ ] Make active-versus-historical run behavior visible if both are exposed.


## `/review/clusters/<id>` detail page


### Header and summary

- [ ] Add a people-style page header with a readable cluster title and cluster
  key/ID.
- [ ] Link the proposed person to `/review/people/<id>`.
- [ ] Show a summary containing:
  - [ ] Face count.
  - [ ] Candidate count.
    * If all faces are assigned-user or assigned-system (which means there are no candidates), put N/A rather than 0
  - [ ] Proposed person.
  - [ ] Consistency
  - [ ] Purity.
  - [ ] Connectivity.
  - [ ] Mixed status.
  - [ ] Review status.
  - [ ] Clustering-run status.
- [ ] Keep the review version visible where it is useful for concurrency
  handling.
- [ ] Add a clear link back to `/review/clusters`.

### Status and warnings

- [ ] Move mixed-cluster warnings into a consistent alert area near the header.
- [ ] Move historical/superseded-run warnings into the same alert treatment.
- [ ] Keep the special mixed-cluster behavior:
  - [ ] Disable bulk default-person assignment.
  - [ ] Require explicit per-face decisions.
- [ ] Make historical-cluster read-only behavior visually obvious.
- [ ] Distinguish review status from clustering-run status.
- [ ] Rathern than "run: <status>", show "Cluster: <status>"

### Styles and layout

- [ ] Styles and layout should be consistent with /review/people/<id>.  Use same css style names for both
- [ ] Move the inline cluster `<style>` block into the page’s `PageStyle`.
- [ ] Establish shared style conventions with the people detail page.
- [ ] Use a consistent responsive grid and card width strategy 
- [ ] Use shared spacing, typography, border, and control styles.
- [ ] Keep cluster-specific styling for mixed state and assignment state.
- [ ] Ensure the page remains usable on narrow screens.

### Review controls

- [ ] Keep the Default person control.
  * Have it preselect the person if there's only one person represented in the cluster
  * If multiple people are represented, leave it as "No default", but put the people represented at the top of the list
  * Adjust so it matches the "Bulk Assignment" form on the /review/people/<id> page.
- [ ] Keep the review Status control.
- [ ] Keep the Note field.
- [ ] Keep the Save review action.
- [ ] Place these controls in a prominent page-action area comparable to the
  people detail page.
- [ ] Keep Save behavior and optimistic/concurrency version handling unchanged
  unless a separate design decision is made.
- [ ] Make the save result/error message visually consistent with the rest of
  the page.

### Detail filters and sorting

- [ ] Add filtering capabilities
  - [ ] Support filtering by assignment state.
  - [ ] Support filtering by assigned person.
  - [ ] Support filtering by disposition.
  - [ ] Support filtering by candidate presence.
  - [ ] Support filtering by possible-candidate presence.
  - [ ] Support quality and detection thresholds.
- [ ] Add sorting by:
  - [ ] Face/path.
  - [ ] Person.
  - [ ] Assignment state.
  - [ ] Disposition.
  - [ ] Consistency
  - [ ] Quality.
  - [ ] Detection.
  - [ ] Candidate confidence.
- [ ] Use the same Apply button and control layout as the person detail page.
- [ ] Preserve filter and sort values in the rendered form.

### Face-card content

- [ ] Keep the photo and face-box overlay.
- [ ] Keep the photo-path link to `/review/photos`.
- [ ] Keep the face identifier.
- [ ] Display the current assignment/person prominently.
- [ ] Display the current assignment state.
- [ ] Display consistency, quality and detection scores using the people-page metadata
  conventions.
- [ ] Display recognition candidates with links to person pages.
- [ ] Display possible candidates when available, using the people-page
  possible-candidate presentation.
- [ ] Keep the face disposition control.
- [ ] Keep the person assignment control.
- [ ] Keep the face-state control.
- [ ] Use consistent assignment-state card border colors.
- [ ] Use consistent card action placement, spacing, actions and input
- [ ] Add an explicit empty-state message when no faces match active detail
  filters.
- [ ] Distinguish “cluster has no faces” from “no faces match the filters.”

### Data and links

- [ ] Link each face’s assigned person to its person detail page.
- [ ] Link each face’s containing photo to the photo review page.
- [ ] Link the cluster key/header to any relevant run or cluster context if
  useful.
- [ ] Show proposed-person and current face-assignment differences clearly when
  they disagree.
- [ ] Preserve candidate ranking, similarity, margin, support, and evidence
  information where available.

## Shared implementation work

- [ ] Identify reusable helpers for page controls, pagination, selected options,
  and filter preservation.
- [ ] Identify reusable CSS classes for review tables, controls, cards, alerts,
  metadata, and empty states.
- [ ] Use existing functions for people-specific actions on cluster pages.
- [ ] Keep cluster review mutations protected by the existing account and CSRF
  checks.
- [ ] Preserve collection access checks for both list and detail pages.
- [ ] Preserve historical-cluster safeguards.
- [ ] Keep original photos and other private derived data out of generated
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

- [ ] Finalize the decisions at the top of this document.
- [ ] Align the cluster list form, styles, selected values, and Apply behavior.
- [ ] Add cluster-list count, pagination, and stable sorting.
- [ ] Align the cluster table presentation and optional preview image.
- [ ] Align the cluster detail header, alerts, and page styles.
- [ ] Align cluster face-card content and links.
- [ ] Add optional cluster-detail filtering and sorting.
- [ ] Add or update regression tests after each group of changes.
- [ ] Run the full Go test suite and `git diff --check`.
