# Align Cluster Review Pages with People Review

## Goal and decisions

- [ ] Align `/review/clusters` and `/review/clusters/<id>` with the people review UI.
- [ ] Preserve cluster-specific mixed-cluster behavior and review safeguards.
- [x] Expose all persisted clustering run statuses, with active runs selected by default.
- [x] Mark all non-active runs visibly and make them read-only.
- [x] For multi-person clusters, show accessible assigned people ordered by assigned-face count and display `Multiple` when applicable.

## Shared review UI

- [ ] Refactor shared helpers for filter/control rendering.
- [ ] Refactor shared helpers for selected options and checkbox state.
- [ ] Refactor shared helpers for pagination and query-parameter preservation.
- [x] Reuse score and unavailable-value formatting.
- [ ] Establish shared CSS classes for controls, tables, cards, alerts, metadata, and empty states.
- [ ] Preserve CSRF, account-role, collection-permission, optimistic-version, mixed-cluster, and historical-run safeguards.

## Cluster list data and behavior

- [x] Scope rows, counts, candidates, filters, and sorting to accessible faces.
- [x] Hide clusters with no accessible faces.
- [x] Add mixed/pure filtering.
- [x] Add review-status filtering.
- [x] Add clustering-run status filtering.
- [x] Add rating filtering.
- [x] Preserve the Include Don’t Track filter.
- [x] Preserve minimum and maximum face-count filters.
- [x] Add minimum and maximum consistency filters.
- [x] Add cluster/person text search covering cluster ID, cluster key, person ID, and person name.
- [x] Render all submitted filter values after reload.
- [x] Use human-readable filter labels.
- [x] Count current recognition candidates with similarity `>= 0.015`.
- [x] Render candidate count as `N/A` when every accessible face is assigned-user or assigned-system.
- [x] Calculate the To Assign count.
- [x] Calculate Assigned Match against the cluster proposed person.
- [x] Aggregate accessible assigned people by face count.
- [x] Add sorting by Mixed.
- [x] Add sorting by Face count.
- [x] Add sorting by Candidate count.
- [x] Add sorting by Person.
- [x] Add sorting by Rating.
- [x] Add sorting by Review status, using unassigned-face count as the secondary key.
- [x] Add sorting by To Be Assigned.
- [x] Add sorting by Cluster key.
- [x] Preserve selected sort and direction.
- [x] Add stable cluster ID tie-breaking.
- [x] Classify rows as Active, Superseded, or Historical.

## Cluster list presentation

- [x] Add a people-style heading with visible and total counts.
- [x] Give the controls form a dedicated ID.
- [x] Match people-page control spacing, wrapping, responsive behavior, and Apply-button treatment.
- [x] Put sorting controls on a separate row below primary filters.
- [x] Add per-page selection.
- [x] Add First, Previous, page number, Next, and Last links.
- [x] Add a page-jump input.
- [x] Clamp invalid and out-of-range page values consistently with the people page.
- [x] Preserve every filter, sort, direction, and pagination parameter in pager links and jump forms.
- [x] Add a dedicated cluster table class.
- [x] Match people-page cell padding, borders, widths, and whitespace behavior.
- [x] Keep the cluster key as the primary row link.
- [x] Link proposed people to `/review/people/<id>`.
- [x] Add representative images before person names.
- [x] Use the person representative when available.
- [x] Fall back to the highest-quality eligible face when needed.
- [x] Keep representative images accessible independently of source-photo permissions.
- [x] Show `Unassigned` when no proposed person exists.
- [x] Render Face count, Candidate count, To Assign, Assigned Match, Consistency, Purity, Connectivity, Mixed, Review, and Type columns.
- [x] Render `X` for mixed clusters and an empty cell otherwise.
- [x] Show Active, Superseded, or Historical in the Type column.

## Cluster detail data and behavior

- [ ] Enforce accessible-face scoping for display and group mutations.
- [ ] Distinguish a cluster with no accessible faces from a cluster where filters match no faces.
- [ ] Load proposed person and clustering-run metadata.
- [ ] Load consistency, purity, connectivity, review status, and review version.
- [ ] Load current assignments and dispositions for accessible faces.
- [ ] Load recognition candidates, possible candidates, and candidate evidence.
- [ ] Use shared representative-image behavior.
- [ ] Preselect the sole represented person as the default person.
- [ ] Leave the default as No default for multi-person clusters.
- [ ] Place represented people at the top of the default-person list.
- [ ] Add filters for assignment state.
- [ ] Add filters for assigned person.
- [ ] Add filters for disposition.
- [ ] Add filters for candidate presence.
- [ ] Add filters for possible-candidate presence.
- [ ] Add numeric min/max quality filters using 0–100 percentages.
- [ ] Add numeric min/max detection filters using 0–100 percentages.
- [ ] Add sorting by face/path.
- [ ] Add sorting by person.
- [ ] Add sorting by assignment state.
- [ ] Add sorting by disposition.
- [ ] Add sorting by consistency.
- [ ] Add sorting by quality.
- [ ] Add sorting by detection.
- [ ] Add sorting by candidate confidence.
- [ ] Preserve all detail filter and sort values after reload.

## Cluster detail presentation and controls

- [ ] Add a people-style header with readable cluster title and key/ID.
- [ ] Link the proposed person to its person detail page.
- [ ] Show Face count, Candidate count, Proposed person, Consistency, Purity, Connectivity, Mixed, Review status, and clustering-run status in the summary.
- [ ] Keep the review version visible where useful for concurrency handling.
- [ ] Add a clear link back to `/review/clusters`.
- [ ] Move mixed-cluster warnings into a shared alert area.
- [ ] Move historical/superseded warnings into the same alert treatment.
- [ ] Disable bulk default-person assignment for mixed clusters.
- [ ] Require explicit per-face decisions for mixed clusters.
- [ ] Make historical read-only behavior visually obvious.
- [ ] Distinguish review status from clustering-run status.
- [ ] Label the run state as `Cluster: <status>`.
- [ ] Move inline cluster styles into `PageStyle`.
- [ ] Reuse people-detail style names and layout conventions.
- [ ] Use a responsive grid and card-width strategy.
- [ ] Reuse shared spacing, typography, borders, and controls.
- [ ] Preserve cluster-specific mixed and assignment styling.
- [ ] Keep the Default person, Status, Note, and Save review controls.
- [ ] Place controls in a prominent people-style page-action area.
- [ ] Preserve save behavior and optimistic/concurrency version handling.
- [ ] Make save success and error messages visually consistent.

## Face-card content

- [ ] Keep the photo and face-box overlay.
- [ ] Keep the photo-path link to `/review/photos`.
- [ ] Keep the face identifier.
- [ ] Display current assignment/person prominently.
- [ ] Display current assignment state.
- [ ] Display consistency, quality, and detection using people-page metadata conventions.
- [ ] Display recognition candidates with person links.
- [ ] Display possible candidates using people-page presentation.
- [ ] Keep disposition, person assignment, and face-state controls.
- [ ] Use consistent assignment-state card border colors.
- [ ] Match people-page card action placement, spacing, and input treatment.
- [ ] Show an explicit empty state when filters match no faces.
- [ ] Show a distinct empty state when the cluster has no accessible faces.
- [ ] Link assigned people to person detail pages.
- [ ] Link containing photos to photo review pages.
- [ ] Link cluster key/header to relevant run context where useful.
- [ ] Show proposed-person versus current-assignment differences.
- [ ] Preserve candidate ranking, similarity, margin, support, and evidence details.

## Tests

- [x] Verify cluster list heading and count.
- [x] Verify all list filters render.
- [x] Verify submitted filters remain selected or populated.
- [x] Verify Apply-button treatment and layout break behavior.
- [x] Verify every list sort option renders.
- [x] Verify selected sort and direction are preserved.
- [x] Verify ascending and descending sorting for every supported sort.
- [x] Verify stable tie-breaking.
- [x] Verify face-count bounds.
- [x] Verify mixed/pure filtering.
- [x] Verify review-status and run-status filtering.
- [x] Verify rating filtering.
- [x] Verify Don’t Track filtering.
- [x] Verify cluster/person search.
- [x] Verify candidate threshold and N/A behavior.
- [x] Verify accessible-face rows, counts, and candidates.
- [x] Verify clusters with no accessible faces are hidden.
- [x] Verify representative-image access and fallback behavior.
- [x] Verify pagination, clamping, page jump, and parameter preservation.
- [ ] Verify detail header and summary fields.
- [ ] Verify proposed-person, person, photo, cluster, and candidate links.
- [ ] Verify mixed-cluster warnings and disabled default assignment.
- [ ] Verify historical warnings and read-only behavior.
- [ ] Verify detail controls render with current values.
- [ ] Verify face cards contain required metadata and actions.
- [ ] Verify recognition and possible-candidate rendering.
- [ ] Verify detail filters and sorting.
- [ ] Verify both detail empty states.
- [ ] Verify collection access enforcement.
- [ ] Verify viewer/editor action differences.
- [ ] Verify CSRF protection.
- [ ] Verify save behavior and review-version conflicts.
- [ ] Run the full Go test suite.
- [ ] Run `git diff --check`.

## Assumptions

- [ ] Treat all persisted run statuses as selectable, with active as the default.
- [ ] Treat queued, running, completed, superseded, failed, and cancelled runs as read-only.
- [ ] Define Historical as any non-active, non-superseded run.
- [ ] Use accessible assigned faces only for assigned-person aggregation.
- [ ] Keep the proposed person in the summary without artificially adding it to the assigned-person list.
- [ ] Use numeric 0–100 percentage inputs for quality and detection filters.
- [ ] Do not add a schema migration unless implementation discovers an existing field is insufficient.
