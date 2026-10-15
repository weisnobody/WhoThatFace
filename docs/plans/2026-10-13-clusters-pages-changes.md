# Cluster Review Pages Alignment Plan

This plan compares the current `/review/people` and
`/review/people/<id>` pages with `/review/clusters` and
`/review/clusters/<id>`, then lists proposed changes to make the cluster pages
consistent with the people review experience while preserving cluster-specific
behavior.

The checkboxes are intentionally left open for revision and prioritization.

## Decisions to make before implementation

- [ ] Decide whether `/review/clusters` should show only active clusters.
  - [ ] Keep the current behavior: show only clusters from the active run.
  - [ ] Add a run/status filter so historical and superseded clusters can be viewed.
  - [ ] Add a separate historical-clusters view or link.
- [ ] Decide how the cluster-list preview image should be selected.
  - [ ] Use the first face in the cluster.
  - [ ] Use the highest-quality face.
  - [ ] Use the highest-detection-confidence face.
  - [ ] Use a persisted/computed cluster representative.
  - [ ] Do not add preview images; keep the table text-only.
- [ ] Decide what “clusters linked to a person” means wherever a person/cluster
  relationship is displayed.
  - [ ] The cluster’s `proposed_person_id` only.
  - [ ] Current face assignments within the cluster only.
  - [ ] Either proposed-person linkage or current assigned-face linkage.
  - [ ] Show separate counts for proposed linkage and assigned-face linkage.
- [ ] Decide whether cluster-detail filtering should be included in the first
  implementation.
  - [ ] Add filtering and sorting to cluster detail now.
  - [ ] Align layout and controls first, then add detail filtering separately.
  - [ ] Keep cluster detail limited to review actions and use the cluster list
    for filtering.
- [ ] Decide whether cluster list pagination should use the people-page values.
  - [ ] Use 10, 20, 25, 50, and 100 items per page.
  - [ ] Use a different set of page sizes.
  - [ ] Do not paginate until cluster volume requires it.
- [ ] Decide whether cluster list search should search both cluster keys and
  proposed-person names.
  - [ ] Add one combined text search field.
  - [ ] Add separate cluster-key and person fields.
  - [ ] Do not add search in the first pass.

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
- [ ] Add cluster/person search according to the decision above.
- [ ] Render all submitted filter values as selected or populated when the
  page reloads.
- [ ] Use human-readable labels instead of raw database values where useful.

### Sorting

- [ ] Keep sorting by Mixed.
- [ ] Keep sorting by Face count.
- [ ] Keep sorting by Candidate count.
- [ ] Keep sorting by Person.
- [ ] Keep sorting by Rating.
- [ ] Consider adding sorting by Review status.
- [ ] Consider adding sorting by Cluster key.
- [ ] Consider adding sorting by Purity.
- [ ] Consider adding sorting by Connectivity.
- [ ] Render the selected sort option after submission.
- [ ] Render the selected ascending/descending direction after submission.
- [ ] Decide whether the default sort should remain Mixed descending or change
  to cluster key, status, or review priority.
- [ ] Use a stable secondary sort, such as cluster key and cluster ID.

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
- [ ] Display an explicit fallback such as `Unassigned` when no proposed person
  exists.
- [ ] Keep Face count.
- [ ] Keep Candidate count.
- [ ] Keep Purity.
- [ ] Keep Connectivity.
- [ ] Keep Mixed status.
- [ ] Keep Review status.
- [ ] Replace raw `true`/`false` mixed values with readable labels or status
  styling.
- [ ] Consider adding an explicit cluster/run status column if historical runs
  become visible.

### Cluster preview and person information

- [ ] Decide whether to add a small face preview to each row.
- [ ] If previews are added, link them to the cluster detail page.
- [ ] Use the selected preview strategy from the decisions section.
- [ ] Indicate when a cluster contains multiple currently assigned people.
- [ ] Distinguish a mixed cluster from a cluster whose proposed person is simply
  unassigned.

### Access and data scope

- [ ] Keep collection-permission filtering consistent with the existing page.
- [ ] Decide how permission filtering should affect preview-face selection.
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
  - [ ] Proposed person.
  - [ ] Purity.
  - [ ] Connectivity.
  - [ ] Mixed status.
  - [ ] Review status.
  - [ ] Clustering-run status.
- [ ] Keep the review version visible where it is useful for concurrency
  handling.
- [ ] Add a clear link back to `/review/clusters`.
- [ ] Consider previous/next cluster navigation if the list has stable
  pagination and sorting.

### Status and warnings

- [ ] Move mixed-cluster warnings into a consistent alert area near the header.
- [ ] Move historical/superseded-run warnings into the same alert treatment.
- [ ] Keep the special mixed-cluster behavior:
  - [ ] Disable bulk default-person assignment.
  - [ ] Require explicit per-face decisions.
- [ ] Make historical-cluster read-only behavior visually obvious.
- [ ] Distinguish review status from clustering-run status.

### Styles and layout

- [ ] Move the inline cluster `<style>` block into the page’s `PageStyle`.
- [ ] Establish shared style conventions with the people detail page.
- [ ] Use a consistent responsive grid and card width strategy.
- [ ] Use shared spacing, typography, border, and control styles.
- [ ] Keep cluster-specific styling for mixed state and assignment state.
- [ ] Ensure the page remains usable on narrow screens.

### Review controls

- [ ] Keep the Default person control.
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

- [ ] Decide whether to add cluster-face filters.
- [ ] If added, support filtering by assignment state.
- [ ] If added, support filtering by assigned person.
- [ ] If added, support filtering by disposition.
- [ ] If added, support filtering by candidate presence.
- [ ] If added, support filtering by possible-candidate presence.
- [ ] If added, support quality and detection thresholds.
- [ ] If added, support sorting by:
  - [ ] Face/path.
  - [ ] Person.
  - [ ] Assignment state.
  - [ ] Disposition.
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
- [ ] Display quality and detection scores using the people-page metadata
  conventions.
- [ ] Display recognition candidates with links to person pages.
- [ ] Display possible candidates when available, using the people-page
  possible-candidate presentation.
- [ ] Keep the face disposition control.
- [ ] Keep the person assignment control.
- [ ] Keep the face-state control.
- [ ] Use consistent assignment-state card border colors.
- [ ] Use consistent card action placement and spacing.
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
- [ ] Avoid duplicating people-specific actions on cluster pages.
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
