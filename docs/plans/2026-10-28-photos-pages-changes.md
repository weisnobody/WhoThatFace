# Implement Photo Review Page Alignment

## Summary

Rebuild `/review/photos` as a people-style, permission-scoped, paginated photo index and align `/review/photos?path_id=<id>` with the people/cluster detail-page presentation.

Rating sorting is excluded because no photo-level rating source is currently defined.

## Key implementation changes

### Photo list query and model

- Extend `internal/search` with a photo-list result model containing accessible face totals, user/system-assigned counts, faces requiring review, assignment/disposition summaries, linked people, candidate/possible-candidate presence, and current cluster context where available.
- Add filters for path/file-name text, accessible collection, faces/all/no-faces, people-page-style assignment controls, Don’t Track/Unknown/Possibles, and person text matching.
- A photo matches when any accessible face satisfies the active face/person condition.
- Preserve the existing collection permission boundary. A photo with no accessible faces is excluded from the list.
- Add stable sorting by face count, person, assigned percentage, and to-be-assigned count, with photo/path ID as the deterministic tie-breaker.

### `/review/photos` page

- Replace the current unpaginated figure list with a people-style heading: `Photos (<visible count>/<total count>)`.
- Add a dedicated `photos-controls` form using the people page’s spacing, wrapping, Apply behavior, selected-value rendering, and responsive rules.
- Add people-style pagination with per-page selection, First/Previous/page/Next/Last links, page jumping, clamped invalid pages, and preservation of all filter, sort, direction, and photo-size parameters.
- Render cards using a dedicated photo-list class and a query-parameter-backed size setting with values `0.25`, `0.5`, `1`, `1.5`, and `2`.
- Each card shows the linked relative path, `Faces: <needs review> / <total>`, numbered linked people with short assignment labels, and `Unassigned` for faces without a linked person.
- Apply green, yellow, or red border states based on the assignment state of the card’s faces.
- Add an explicit empty state for no matching photos.

### Photo detail page

- Keep `/review/photos?path_id=<id>` as the canonical detail URL.
- Require access to the photo’s collection, then render every face belonging to that accessible photo.
- Replace the “Photo review” heading with the relative path.
- Add a people-style summary containing user/system-assigned faces / total faces, linked people, and accessible collection links back to `/review/photos?collection_id=...`.
- Move inline photo styling into shared page styles and use the people/cluster responsive card conventions.
- Add the same query-parameter-backed photo-size controls as the list page.
- Show the photo-level review count after the file name and add face numbers beside bounding boxes.
- For user/system-assigned faces, render only `Face <#>: <Person Name> [User/System]`; omit assignment, disposition, Reference, and Don’t Track controls.
- For all other faces, reuse cluster-detail rendering conventions: explicit person fallback, recognition and possible candidates, current cluster or explicit `No cluster`, remaining supported review actions, and the assignment form.
- Remove Reference and Don’t Track actions from the photo detail UI.
- Link every displayed person to `/review/people/<id>`.
- Link current cluster context when available without exposing inaccessible original photos.

### Shared access and image behavior

- Preserve account and CSRF checks for all mutations.
- Keep original-photo access restricted to accessible collections.
- Allow linked person names and representative images to render without requiring access to the source photo used to generate the representative image.
- Do not broaden original-photo access through representative-image endpoints.

### Tests and cleanup

- Rename the test section from “Cluster list tests” to “Photo list tests.”
- Remove copied cluster-only requirements such as mixed-cluster save/version tests and preview selection unless separately specified.
- Add search-layer tests for each filter and OR behavior, broad person matching, accessible-face scoping, all supported sort directions and stable ties, aggregate counts, and no-face behavior.
- Add web tests for heading/count, controls and selected values, photo-size persistence, card metadata and border states, pagination URL preservation, detail summary and collection links, assigned versus unresolved face-card controls, candidate/cluster/no-cluster rendering, viewer/editor behavior, and collection permission enforcement.
- Use synthetic database rows only; do not add photos, embeddings, or generated private data.
- Run the focused search/web tests, the full Go test suite, and `git diff --check`.

## Assumptions

- People-page controls and defaults are the source of truth for assignment, status, and OR semantics.
- “Any face in the photo” applies to detail rendering once the photo itself is collection-accessible.
- The photo list’s default remains the existing review-oriented scope unless the people-style controls explicitly select broader face coverage.
- The person/collection alternate-name and representative-image feature is future work and is not part of this implementation.
