# Align Cluster Review Pages with People Review

## Goal and decisions

- [x] Align `/review/clusters` and `/review/clusters/<id>` with the people review UI.
- [x] Preserve cluster-specific mixed-cluster behavior and review safeguards.
- [x] Expose all persisted clustering run statuses, with active runs selected by default.
- [x] Mark all non-active runs visibly and make them read-only.
- [x] For multi-person clusters, show accessible assigned people ordered by assigned-face count and display `Multiple` when applicable.

## Shared review UI

- [x] Refactor shared helpers for filter/control rendering.
- [x] Refactor shared helpers for selected options and checkbox state.
- [x] Refactor shared helpers for pagination and query-parameter preservation.
- [x] Reuse score and unavailable-value formatting.
- [x] Establish shared CSS classes for controls, tables, cards, alerts, metadata, and empty states.
- [x] Preserve CSRF, account-role, collection-permission, optimistic-version, mixed-cluster, and historical-run safeguards.

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

- [x] Enforce accessible-face scoping for display and group mutations.
- [x] Distinguish a cluster with no accessible faces from a cluster where filters match no faces.
- [x] Load proposed person and clustering-run metadata.
- [x] Load consistency, purity, connectivity, review status, and review version.
- [x] Load current assignments and dispositions for accessible faces.
- [x] Load recognition candidates, possible candidates, and candidate evidence.
- [x] Use shared representative-image behavior.
- [x] Preselect the sole represented person as the default person.
- [x] Leave the default as No default for multi-person clusters.
- [x] Place represented people at the top of the default-person list.
- [x] Add filters for assignment state.
- [x] Add filters for assigned person.
- [x] Add filters for disposition.
- [x] Add filters for candidate presence.
- [x] Add filters for possible-candidate presence.
- [x] Add numeric min/max quality filters using 0–100 percentages.
- [x] Add numeric min/max detection filters using 0–100 percentages.
- [x] Add sorting by face/path.
- [x] Add sorting by person.
- [x] Add sorting by assignment state.
- [x] Add sorting by disposition.
- [x] Add sorting by consistency.
- [x] Add sorting by quality.
- [x] Add sorting by detection.
- [x] Add sorting by candidate confidence.
- [x] Preserve all detail filter and sort values after reload.

## Cluster detail presentation and controls

- [x] Add a people-style header with readable cluster title and key/ID.
- [x] Link the proposed person to its person detail page.
- [x] Show Face count, Candidate count, Proposed person, Consistency, Purity, Connectivity, Mixed, Review status, and clustering-run status in the summary.
- [x] Keep the review version visible where useful for concurrency handling.
- [x] Add a clear link back to `/review/clusters`.
- [x] Move mixed-cluster warnings into a shared alert area.
- [x] Move historical/superseded warnings into the same alert treatment.
- [x] Disable bulk default-person assignment for mixed clusters.
- [x] Require explicit per-face decisions for mixed clusters.
- [x] Make historical read-only behavior visually obvious.
- [x] Distinguish review status from clustering-run status.
- [x] Label the run state as `Cluster: <status>`.
- [x] Move inline cluster styles into `PageStyle`.
- [x] Reuse people-detail style names and layout conventions.
- [x] Use a responsive grid and card-width strategy.
- [x] Reuse shared spacing, typography, borders, and controls.
- [x] Preserve cluster-specific mixed and assignment styling.
- [x] Keep the Default person, Status, Note, and Save review controls.
- [x] Place controls in a prominent people-style page-action area.
- [x] Preserve save behavior and optimistic/concurrency version handling.
- [x] Make save success and error messages visually consistent.

## Face-card content

- [x] Keep the photo and face-box overlay.
- [x] Keep the photo-path link to `/review/photos`.
- [x] Keep the face identifier.
- [x] Display current assignment/person prominently.
- [x] Display current assignment state.
- [x] Display consistency, quality, and detection using people-page metadata conventions.
- [x] Display recognition candidates with person links.
- [x] Display possible candidates using people-page presentation.
- [x] Keep disposition, person assignment, and face-state controls.
- [x] Use consistent assignment-state card border colors.
- [x] Match people-page card action placement, spacing, and input treatment.
- [x] Show an explicit empty state when filters match no faces.
- [x] Show a distinct empty state when the cluster has no accessible faces.
- [x] Link assigned people to person detail pages.
- [x] Link containing photos to photo review pages.
- [x] Link cluster key/header to relevant run context where useful.
- [x] Show proposed-person versus current-assignment differences.
- [x] Preserve candidate ranking, similarity, margin, support, and evidence details.

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
- [x] Verify detail header and summary fields.
- [x] Verify proposed-person, person, photo, cluster, and candidate links.
- [x] Verify mixed-cluster warnings and disabled default assignment.
- [x] Verify historical warnings and read-only behavior.
- [x] Verify detail controls render with current values.
- [x] Verify face cards contain required metadata and actions.
- [x] Verify recognition and possible-candidate rendering.
- [x] Verify detail filters and sorting.
- [x] Verify both detail empty states.
- [x] Verify collection access enforcement.
- [x] Verify viewer/editor action differences.
- [x] Verify CSRF protection.
- [x] Verify save behavior and review-version conflicts.
- [x] Run the full Go test suite.
- [x] Run `git diff --check`.

## Assumptions

- [x] Treat all persisted run statuses as selectable, with active as the default.
- [x] Treat queued, running, completed, superseded, failed, and cancelled runs as read-only.
- [x] Define Historical as any non-active, non-superseded run.
- [x] Use accessible assigned faces only for assigned-person aggregation.
- [x] Keep the proposed person in the summary without artificially adding it to the assigned-person list.
- [x] Use numeric 0–100 percentage inputs for quality and detection filters.
- [x] Do not add a schema migration unless implementation discovers an existing field is insufficient.
