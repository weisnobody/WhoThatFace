# Simplified Human-Review Model Settings Protocol

## Purpose

This protocol is an easier, review-oriented alternative to the formal
evaluation protocol in `identify_model_settings.md`. It is intended to answer:

- Did the system find the faces a person would expect it to find?
- Did it group or identify faces belonging to the same person correctly?
- When the result is uncertain, were the suggested candidates reasonable?
- Are the proposed similarity, support-count, and quality settings useful in
  practice?

It is suitable for choosing prototype or MVP settings, but it is less rigorous
than a locked development/calibration/final evaluation process.

## Recommended dataset

Use one private dataset with approximately:

- **15–20 known people**.
- **250–400 total photos**.
- **5 reference photos per known person**: approximately 75–100 photos total.
- **8–12 evaluation photos per known person**: approximately 120–240 photos
  total.
- **30–50 photos containing people who are not in the known-person set**, when
  available.

The remaining photos may contain multiple known people, unknown people, no
faces, difficult poses, blur, occlusion, varied lighting, and different photo
sessions. These conditions are useful because the goal is to evaluate the
review experience, not only ideal portrait images.

If assembling 250–400 photos is impractical, begin with at least 10 people,
100 total photos, 3 reference photos per person, and 5 evaluation photos per
person. Treat the results as exploratory.

## Preparing the reference and evaluation sets

The application may process all photos in one run. The separation is about how
the results are used, not necessarily about separate imports.

1. Select approximately five varied reference photos for each known person.
2. Review those photos with model identity suggestions hidden.
3. Label the reference faces and create the corresponding people.
4. Use those confirmed faces as matching examples.
5. Review and label the evaluation photos independently, still with model
   suggestions hidden.
6. Reveal the model's candidates, scores, margins, support counts, and quality
   indicators.
7. Record whether each suggestion was correct, incorrect, missing, too
   cautious, or appropriate as unknown.

Do not use the same face instance in both sets. Avoid exact duplicates,
near-duplicate crops, and burst photos of the same moment across the boundary.
Reference photos should represent different sessions or conditions from the
evaluation photos where practical.

Unknown people belong in the evaluation set only. Do not add them as examples
for a known person.

## Review states

During the blind review, each detected face should be assignable to one of:

- a known person;
- an unknown person;
- an ambiguous or unusable face; or
- a false detection.

The reviewer should also be able to record a missed face if a photo contains a
face that the detector did not find. A person may be represented by an
internal label such as `Person 01` until the reviewer chooses to provide a
name.

The model's answer must remain hidden until the reviewer has made the ground-
truth decision. Otherwise, correcting a model suggestion can unintentionally
become confirmation of that suggestion.

## What to look for

For detection:

- missed faces;
- false detections;
- poor or partial bounding boxes;
- failures by image orientation, size, lighting, pose, or occlusion.

For recognition and grouping:

- correct person at rank one;
- correct person somewhere in the candidate list;
- incorrect high-confidence assignment;
- reasonable candidates but insufficient confidence;
- unknown faces correctly left unresolved;
- faces incorrectly grouped with another person;
- one person split into several provisional groups;
- multiple people combined into one provisional group.

Pay particular attention to whether an incorrect assignment would be easy to
notice and correct. A system that asks for review on difficult faces is safer
than one that makes confident-looking errors.

## Suggested outputs

The review tool should produce a report containing:

- dataset and image counts;
- detected, missed, and false-detected face counts;
- correct, incorrect, and unresolved identity decisions;
- correct-person-in-top-k counts;
- results grouped by face-quality bucket;
- results grouped by number of supporting examples;
- examples of the most useful and most misleading suggestions;
- provisional-cluster split and merge issues;
- proposed threshold changes and the reason for each change.

The report should retain the model, pipeline, and settings identifiers so that
two review sessions can be compared.

## Interpretation

Use the results to choose settings and identify workflow problems, not to claim
general-purpose accuracy. Repeat the review with a different set of photos
before treating a setting as stable. Keep a small untouched subset when making
the final choice so that repeated threshold adjustments do not gradually tune
to the same examples.

