# Review of Assignment and Face-Review Functionality

Active Discussion (as of 2026-10-30)

## Prompt ##

1. When a user uses the "unknown" buttons what happens to the assignemnt state / person link?  The intention was to mark this face as unknown (we don't know who it is / it most likely won't be a face we are interested in.  Thinking about what we wanted, it is likely similar to "Don't Track", but at least one level less important than "Don't Track" (Don't track we may know the person's name but we don't current care about the person to have them show up in the "to be reviewed" set).  If new faces show up that match them, we want them linked to the same person. Unknown says we have no idea who this person is, they may show up in other photos, and while if they do we want thoses faces grouped together, we don't want them in the active review list.  Looking at it this way, should we treat them both as "Don't Track", and one may have a name assigned to them and the "unknowns" don't have a name?
2. Similarily, there's faces that we know they are someone we known (and are tracking) but we're not sure which person they are.  These we'd
  use the Ambigious flag for, and perhaps as more reference photos are added to people the face will be identified.  This was intended for cases
  where the face was not clearly visible and at present, neither the system nor the review can figure out who they are.
3. A third classification are faces that we can't tell who they are and we want to tag them so we can ask someone / follow up on them after there is more data.  This is possibly related to the ambigious flag, but it is more of I don't know who the person is (old photo / don't recognize the person, etc.). but I want to track them down.
4. The "Incorrect Assignment" button is another one where the intention was to flag the face is not the person the system suggested, but doesn't provide a next step.  This may more correctly be something that should be used when all of the following is true: the system assigns a face, the user says it's the wrong person, and the user doesn't know who the correct person is.
5. "False detection" is currently described as "the detected region is not a usable face", but it should be changed to the "the detected region is not a face" (it's not actually a person).
6. "Unassigned" as a concept is vague and likely originated before we set things up so that if there's a face that isn't matched with an existing person that a new person is created for it.

Let's review the behavior of the "Unknown", "Don't Track", "Ambigious", "Incorrect Assignment" buttons / actions as well as the concepts of "Unassigned" faces.  Review the DESIGN.md and SPEC.md files, the code and schema, my statements above and have a discussion about what we originally intended, what is implemented, and possible changes / corrections based on what we've learned using the system.


## Summary

The current model has a useful foundation: assignment state, face disposition,
and person state are separate. However, several labels and transitions no
longer match the intended workflow.

The largest mismatches are:

- `Unknown` removes the person link but remains in the active review set. The
  intended behavior is closer to “unknown and not currently interesting,”
  while still allowing grouping with similar unknown faces.
- `Incorrect Assignment` records a disposition but does not clear the wrong
  person assignment.
- `Ambiguous` overlaps two different concepts: a likely tracked person whose
  identity is unclear, and an unknown person worth investigating.
- `Unassigned` is an operational/transitional state, but is presented too much
  like a meaningful user classification.
- `False Detection` is described too broadly. It should mean that the region is
  not a face, not merely that the face is unusable.

## Current behavior

### Unknown

The Unknown button sends:

```json
{"person_id": "", "state": "unknown"}
```

This changes `current_face_assignments` to:

```text
state = unknown
person_id = NULL
```

Unknown removes the person link and does not create or preserve a person
record. Unknown faces remain eligible for grouping and later recognition. A
reference change may later move one to `provisional` or `assigned-system`.
They also remain in the default review-oriented result set.

Relevant implementation areas include `internal/web/server.go`,
`internal/review/store.go`, and `schemas/migrations/001_initial.sql`.

### Ambiguous

Ambiguous also stores no person link:

```text
state = ambiguous
person_id = NULL
```

The intended processing difference is that automatic recognition must not
silently change an explicitly ambiguous face, although new candidates may be
shown later. This is a good fit for a face that is probably someone in the
tracked population but cannot currently be identified.

The current label is too broad because it also covers an unknown person that
the user wants to investigate.

### Don't Track

Don't Track is not an assignment state. It is an independent face disposition:

```text
current_face_dispositions.disposition = 'dont-track'
```

It does not remove an existing person assignment. A face can therefore have:

```text
assignment: assigned-user, person_id = Alice
disposition: dont-track
```

or:

```text
assignment: unknown, person_id = NULL
disposition: dont-track
```

Don't Track faces are excluded from ordinary/default lists, but remain
available to grouping, recognition, and explicitly included searches.

### Incorrect Assignment

The current Incorrect Assignment button only adds the
`incorrect-assignment` disposition. It does not change the assignment state or
remove the person link.

If the system assigned Alice and the user clicks Incorrect Assignment, the
current state may remain:

```text
state = assigned-system
person_id = Alice
disposition = incorrect-assignment
```

This conflicts with the documented rejection behavior, which says that
rejecting an assignment without a replacement restores the previous state,
normally `unassigned` or `provisional`, while retaining the rejection history.

### False Detection

The database/action name is correctly `false-detection`, but the current
description is “the detected region is not a usable face.” That is too broad.
A blurry, partial, or otherwise difficult face is still a face and may be worth
tracking.

The better definition is:

> The detected region is not a face.

### Unassigned

`Unassigned` currently covers several situations:

- a newly detected face before processing finishes;
- a rejected assignment with no replacement;
- an undo fallback;
- a face that has not yet received a user decision.

It is therefore an operational state, not a single meaningful user
classification.

## Recommended conceptual model

Keep three dimensions separate:

| Dimension | Meaning |
| --- | --- |
| Identity assignment | Which person, if any, is linked to this face? |
| Review/follow-up status | Does this face need human attention later? |
| Tracking disposition | Should it participate in normal user-facing tracking and review? |

### Unknown, not currently interesting

Meaning:

> We do not know who this person is, and we do not currently care enough to investigate.

Recommended result:

```text
assignment state: unknown
person link: none
tracking disposition: dont-track
```

Unknown answers whether we know who the person is; Don't Track answers whether
the face should appear in normal active work. Unknown faces should still be
groupable with other similar faces. If multiple unknown faces appear to be the
same individual, they should remain in the same provisional group or receive a
stable anonymous group identity. They should not automatically become a named
`people` record unless the user later chooses to track them.

The UI could call this action “Unknown — Don’t Track,” while retaining
`unknown` and `dont-track` as separate stored concepts.

### Ambiguous, likely tracked person

Meaning:

> This is probably someone from the tracked population, but the evidence is insufficient to identify which person.

Recommended result:

```text
assignment state: ambiguous
person link: none
tracking disposition: active
```

Ambiguous faces should remain visible in the active review/follow-up queue.
New recognition candidates may be displayed, but automatic processing should
not silently assign one.

### Unknown but worth investigating

Meaning:

> We do not know who this person is, but we want to find out later.

This is not the same as Ambiguous; the person may not yet be represented in the
people database. A useful model is:

```text
identity state: unknown
follow-up status: needs-identification
tracking disposition: active
```

Possible labels are “Needs Identification,” “Follow Up,” or “Unknown — Track.”
“Needs Identification” is clearest because it describes the work without
implying that the person is already known to the system. It should remain
groupable and searchable, appear in active review, and support later promotion
to a named person. It could also support notes such as “old photo,” “ask
family,” or “not recognized.”

## Don't Track should remain independent

Unknown and Don't Track should not be collapsed into one stored state:

- A known person can be Don't Track.
- An unknown person can be Don't Track.
- An unknown person can be actively tracked for identification.
- An ambiguous person is normally active and not Don't Track.
- A false detection is not an unknown person.

The current schema already supports this separation. The main correction is
the default behavior and UI semantics: Unknown currently remains active by
default, while the intended low-interest workflow is closer to Unknown plus
Don't Track.

## Incorrect Assignment should be an action

There are two cases.

### The correct person is known

The user should assign the correct person directly. The system should record
that the prior suggestion was rejected, and the face should end as:

```text
state = assigned-user
person_id = correct-person
```

### The correct person is not known

The user should use an action equivalent to “Reject assignment; identify later.”
That action should:

1. restore or set the face to `unknown`, `ambiguous`, or
   `needs-identification`;
2. clear the current person link;
3. retain the rejected proposed person in assignment history;
4. record an incorrect-assignment event;
5. prevent the same rejected suggestion from repeatedly resurfacing without new
   evidence.

The current standalone disposition does not perform the first two steps. It
leaves the wrong person link in place, which is misleading for search and
reference recommendations.

The UI should either rename the action to “Reject Assignment” or “Wrong
Person,” or make it a compound action that requires a destination state when
no replacement person is selected. `incorrect-assignment` can remain valuable
as historical audit information, but should not be the only current face
disposition after the assignment has been cleared.

## False Detection correction

Change the SPEC, DESIGN, UI help text, and tests from:

> The detected region is not a usable face.

to:

> The detected region is not a face.

A blurry or unusable face should use Ambiguous or Needs Identification,
depending on whether it is worth following up. False Detection is only for a
detector error.

## Unassigned should become mostly internal

Retain `unassigned` in the database because it is useful during processing and
undo, but reduce its user-facing role:

- Newly detected faces may temporarily be `unassigned`.
- Once recognition/grouping completes, they should become `unknown`,
  `provisional`, or `assigned-system`.
- A user rejection should immediately choose a meaningful destination instead
  of leaving the face unassigned.
- Undo may restore `unassigned`, but the UI should explain that it means no
  current decision is recorded.
- The active review list should distinguish “Unprocessed” from “Unknown.”

## Proposed changes

1. Keep assignment states separate from dispositions.
2. Define Unknown as “identity not known.”
3. Make “Unknown — Don’t Track” the default low-interest workflow.
4. Keep Ambiguous for “probably tracked, but identity cannot currently be
   determined.”
5. Add “Needs Identification” for unknown people worth following up on.
6. Make Incorrect Assignment clear the wrong person link when no replacement is
   supplied.
7. Preserve rejected suggestions in history and audit data.
8. Change False Detection to “the region is not a face.”
9. Treat Unassigned as an internal/transitional state, not a final user
   classification.
10. Ensure unknown faces can still be clustered/grouped without creating a
    named person.

The most urgent implementation mismatch is Incorrect Assignment: the UI
records the complaint but can leave the wrong person assignment in place. The
largest semantic mismatch is Unknown: the code treats it as an active review
state, while the intended workflow treats it as an anonymous person normally
excluded from active review unless explicitly marked for follow-up.

## Conclusion ##

IN PROGRESS

1. Assignments
  * Unassigned: (Mostly) Internal only, temporary state before recognition/grouping completes
  * Assigned Wrong: Face was previously provisonally, system or user assigned but user indicated assignment was incorrect and was unable to fall back to a different person.
  * Provisional: System thinks that this face belongs to this person
  * System Assigned: System has decided that this face belongs to this person
  * User Assigned: User has indicated this face belongs to this person
2. Pseudo assignments
  * Unknown: Users don't know name of person.  People with no name can be considered unknown (no other database field needs to be involved). No need to track this.
2. Dispositions
  * Not a Face: The region identified as a face is not a face. Was "False Detection".
  * Don't Track: We're not paying attention to this person
  * Ambiguous: Neither the system nor the user are sure which person the face belongs to.  This is typically a manner of the face being obscured, partially blocked, etc
  * TBD: Person name is to be determined. This is a proactive form of "Unknown" in that the user is indicating they want to come back to this person and determine who they are
  * No Face: This currently is not implemented anywhere.  This is something to be considered in the future.  Would be used if system can't tell that there is a person / face in the photo but the user wants to flag that the person is in the photo.  These photos might (in the future) be included in the people detail and photo list when searching for a person.  The more useful application would be when there's a method to ask: "Show me all of the photos that have person A, person B, and person C in them."
3. 