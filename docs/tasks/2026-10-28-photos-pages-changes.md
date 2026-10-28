# Photos Review Pages Alignment Task List

These tasks work to bring the functionality and look of the Photos review pages to align with the people and clusters pages.


## `/review/photos` list page

### Page structure and layout

The /rev

- [ ] Add a people-style heading with the visible count and total count, for
  example `Photos (10/47)`.
- [ ] Give the controls form a dedicated ID and page-specific style rules.
- [ ] Match the people page’s control spacing, wrapping, and responsive layout.

### Filters

- [ ] Add filters for:
  - [ ] Path / File name input field
  - [ ] Collection pulldown (All or an individual collection that has photos)
  - [ ] Photos with / without faces, all (default is All)
  - [ ] Assignment types (pulldown with the assignment types, including All (default).  When something other than all is selected, only photos with at least 1 face of the specific assignment type will be included in the list
  - [ ] Pulldown to filter list to filter by photos that at least 1 face  is "Don't Track", Unknown, Possibles (has face with possible candidates identified)
  - [ ] Person (input field) to filter to photos with a face that may be a person with the text in their name / id.
- [ ] Render all submitted filter values as selected or populated when the
  page reloads.
- [ ] Use human-readable labels instead of raw database values where useful.
- [ ] Add radio buttons to alter the photo size displayed in the list (1/4, 1/2, 1, 1.5, 2) (default to 1 using the current size)

### Sorting

Add sorting, including option for ascending / descending by:

- [ ] Face count.
- [ ] Person.
- [ ] Rating.
- [ ] Assigned (sort by the percentage of faces that are user / system assigned)
- [ ] "To Be Assigned" (the number of faces that are not assigned-user / assigned-system)
- [ ] Render the selected sort option after submission.
- [ ] Render the selected ascending/descending direction after submission.
- [ ] Use a stable secondary sort, such as photo ID.

### Pagination

Pagination should look / work the same as on the people list page

- [ ] Add per-page selection.
- [ ] Add First, Previous, page number, Next, and Last links.
- [ ] Add a page-jump input.
- [ ] Clamp invalid or out-of-range page values consistently with the people
  page.
- [ ] Preserve active filters and sort options in every pagination URL.


### Table presentation

Use the same table / card layout as used by the people details page, allowing mulitple photos per row if space permits.

- [ ] Add a dedicated table class matching the people page’s table styling.
- [ ] Use consistent cell padding, borders, width behavior, and whitespace.
- [ ] Move the "(<count> faces)" text to the next line and add a count of faces in the photo that need review and the total count of faces i.e. "Faces: 3 / 4; "
- [ ] After the faces count (same line), show the list of persons linked to the faces like "1. <person name> (<assignment>), 2. <person name> (assignment)".
    * The person's name should be a link to that person's detail page
    * If no person is linked, show "Unassigned" as the name with no link
    * Use "System", "User", "Prov" rather than the full type
- [ ] Card border color should match that of the people cards.
  * Green only if all faces are user or system assigned
  * Yellow if all faces are provisionally assigned are better
  * Red if any face as an assignment other than user-assigned, system-assigned, or provisional


### Access and data scope

- [ ] Keep collection-permission filtering consistent with the existing page.
- [ ] Photos / Faces
  * If user doesn't have access to any faces in a photo, the photo should not show up in the list
  * Counts on the photo list page should not include photos that the user does not have access to
 - [ ] representative-images are accessable by any user even if they don't have access to the photo the face comes from
- [ ] Ensure counts and candidate totals use the same accessible face scope as
  the rows being displayed.


## `/review/photos/<id>` / `/review/photos?path_id=<id>` detail page


### Header and summary

- [ ] Replace the "Photo review" text with the relative path of the file
- [ ] On the next line, have a table like on the people pages with columns for:
  * Faces, with detail being the `<number of faces user/system assigned> / <total faces>`
  * People, with detail being a comma seperated list of person names linked to faces in the photo (with links to their person detail page)
  * Collections, with detail being a list of collection names that the photo is included in
    * Each collection should be a link that opens the photos list page for that collection

### Styles and layout

- [ ] Styles and layout should be consistent with /review/people/<id>.  Use same css style names for both
- [ ] Establish shared style conventions with the people detail page.
- [ ] Use a consistent responsive grid and card width strategy
- [ ] Use shared spacing, typography, border, and control styles.
- [ ] Ensure the page remains usable on narrow screens.

### Detail filters and sorting

No filtering / sorted needed, but 

- [ ] Add radio buttons to alter the photo size displayed in the list (1/4, 1/2, 1, 1.5, 2) (default to 1 using the same size as the photos detail page)
 

### Face-card content

- [ ] Display an explicit fallback such as `Unassigned` when no proposed person
  exists.
- [ ] Remove the "Reference"  and "Don't track" buttons
- [ ] After the file name add a count of faces in the photo that need review and the total count of faces " (3 / 4)"
- [ ] Add the face number next ot the bounding box for the face, near the top right corner. 
- [ ] If face is system or user assigned, just show `Face <#>: <Person Name> [<assignment type>]`
    * No buttons / forms should be shown
- [ ] For other faces, content should match the content from the /review/clusters/<id> page for a face, 
    * Modify the first line so it is just: `Face <#>: <Person Name> [<assignment type>]"
    * Next is the list of candidates
    * Then the cluster
    * Then the buttons
    * Last is form for person assignment


### Data and links

- [ ] Link each person name shown to its person detail page.
- [ ] Link the cluster key/header to any relevant run or cluster context if
  useful.

## Shared implementation work

- [ ] Reuse existing helpers from the people and clusters details pages for page controls, pagination, selected options, and filter preservation.
- [ ] Reuse CSS classes for review tables, controls, cards, alerts, metadata, and empty states.
- [ ] Use existing functions for people and cluster specific actions
- [ ] Keep cluster and people review mutations protected by the existing account and CSRF
  checks.
- [ ] Preserve collection access checks for both list and detail pages.
- [ ] Keep original photos and other private derived data out of generated fixtures and committed test assets.

## Tests

### Cluster list tests

- [ ] Verify the photo page renders its heading and count.
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

### Photo detail tests

- [ ] Verify the header summary fields.
- [ ] Verify proposed-person and photo/person links.
- [ ] Verify mixed-cluster warnings and disabled default assignment.
- [ ] Verify photo controls render with the correct current values.
- [ ] Verify face cards contain required metadata and actions.
- [ ] Verify candidate and possible-candidate rendering.
- [ ] Verify detail filters and sorting, if added.
- [ ] Verify empty-state behavior.
- [ ] Verify collection access enforcement.
- [ ] Verify viewer/editor action differences.
- [ ] Verify Save review behavior remains protected and version-aware.

## Suggested implementation order

- [ ] Align the photo list form, styles, selected values, and Apply behavior.
- [ ] Add photo-list count, pagination, and stable sorting.
- [ ] Align the photo table presentation and optional preview image.
- [ ] Align the phto detail header, alerts, and page styles.
- [ ] Align photo face-card content and links.
- [ ] Add photo size form
- [ ] Add or update regression tests after each group of changes.
- [ ] Run the full Go test suite and `git diff --check`.
