# Job Recovery and System Interruption Handling

## Summary

Make background jobs recoverable after disk exhaustion, worker termination, or
another system-level interruption. User pauses and system interruptions remain
distinct while using the existing `paused` status for schema compatibility.

## Behavior

- Add `jobs.pause_reason`, with `none`, `user`, and `system` values; existing
  rows use `none`.
- Add `job recover JOB_ID --yes` for jobs stuck in `running` or
  `cancelling`. Recovery requires explicit confirmation that the worker is no
  longer active.
- Add `job finalize-cancel JOB_ID --yes` for an abandoned `cancelling` job;
  preserve completed items, mark unfinished items skipped, and finalize the
  job as `cancelled`.
- Recovery changes the job to `paused`, sets `pause_reason=system`, records the
  interruption reason, and returns running item claims to `pending`.
- `job resume JOB_ID` resumes both user-paused and system-interrupted jobs.
- `job retry JOB_ID` remains an explicit whole-job retry: all job items are
  reset to pending and rerun from the original payload.
- Disk-full, worker termination, and similar operational failures must not
  retry indefinitely. They remain visible for operator action.
- Job output and status inspection expose the pause reason.

## Implementation

- Add an immutable schema migration for `pause_reason`.
- Extend the jobs store and CLI control paths with system recovery and pause
  reason handling.
- Audit explicit recovery actions as job-level administrative events.
- Preserve completed derived data and job history; recovery only changes job
  control state and incomplete item claims.

## Tests

- User pause/resume stores and clears the user pause reason.
- Recovery accepts only running/cancelling jobs and moves them to system
  paused with running items pending.
- Resume clears the system pause reason and makes the job claimable.
- Whole-job retry resets every item, including completed items.
- CLI recovery requires `--yes` and records an audit event.
- Finalize-cancel accepts only cancelling jobs, preserves completed items, and
  records an audit event.
- Existing jobs migrate with `pause_reason=user`.

## Assumptions

- A system-paused job is not automatically resumed; the operator resolves the
  underlying issue first.
- Automatic heartbeat/lease detection is deferred until explicit recovery is
  proven in use.
