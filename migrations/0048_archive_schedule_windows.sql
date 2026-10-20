-- Archive schedules become time windows. ARC-08.
--
-- The old format was a list of daily fire times ("02:00,14:30"), and one fire
-- enqueued one bounded batch. A channel that accumulated a thousand overnight
-- segments therefore needed ten days to drain, because the ledger's UNIQUE
-- (policy, date, time) row allowed exactly one batch per fire time per day.
--
-- A window ("02:00-06:00") says what the operator actually means: while it is
-- open, keep feeding the queue. The scheduler now re-scans on every tick and
-- stops when the window closes.

-- Rewrite existing rows BEFORE tightening the constraint, otherwise the rows the
-- old code wrote are rejected by the new rule.
--
-- A single fire time T becomes the one-minute window T..T+1min. That preserves
-- the old behaviour exactly (one trigger, now able to drain up to a tick's
-- ceiling instead of one batch) and stays visibly a point in the editor, so
-- widening it is a deliberate act rather than something this migration guessed.
UPDATE archive_policies
SET schedule = (
  SELECT string_agg(
           fire || '-' || to_char(
             (fire::time + interval '1 minute')::time,
             'HH24:MI'
           ),
           ',' ORDER BY ordinality
         )
  FROM unnest(string_to_array(schedule, ',')) WITH ORDINALITY AS parts(fire, ordinality)
)
WHERE mode = 'scheduled'
  AND schedule <> ''
  AND schedule NOT LIKE '%-%';

-- Windows: HH:MM-HH:MM, optionally several separated by commas. Empty stays
-- allowed because the non-scheduled modes store no schedule.
ALTER TABLE archive_policies DROP CONSTRAINT IF EXISTS archive_policies_schedule_check;
ALTER TABLE archive_policies ADD CONSTRAINT archive_policies_schedule_check
  CHECK (schedule = '' OR schedule ~ '^[0-2][0-9]:[0-5][0-9]-[0-2][0-9]:[0-5][0-9](,[0-2][0-9]:[0-5][0-9]-[0-2][0-9]:[0-5][0-9])*$');

-- archive_schedule_runs is deliberately kept, not dropped: the window scheduler
-- no longer claims slots (it evaluates on every tick, and the enqueue itself is
-- idempotent per segment+target), so nothing writes to it. Leaving the old rows
-- in place costs nothing and keeps the history readable while this change beds
-- in. Dropping it would destroy evidence for a rollback.
