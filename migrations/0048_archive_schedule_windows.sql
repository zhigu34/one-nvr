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

-- Drop the old rule FIRST. The rewrite below produces values the old CHECK
-- rejects (it only allows "HH:MM,HH:MM"), so updating the rows before dropping
-- it fails with SQLSTATE 23514 — which is exactly what happened on the first
-- deployment of this migration.
ALTER TABLE archive_policies DROP CONSTRAINT IF EXISTS archive_policies_schedule_check;

-- Rewrite existing rows to one-minute windows: a single fire time T becomes
-- T..T+1min. That preserves the old behaviour (the same instant triggers, now
-- able to drain up to a tick's ceiling instead of one batch) and stays visibly a
-- point in the editor, so widening it is a deliberate act rather than something
-- this migration guessed.
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
ALTER TABLE archive_policies ADD CONSTRAINT archive_policies_schedule_check
  CHECK (schedule = '' OR schedule ~ '^[0-2][0-9]:[0-5][0-9]-[0-2][0-9]:[0-5][0-9](,[0-2][0-9]:[0-5][0-9]-[0-2][0-9]:[0-5][0-9])*$');

-- archive_schedule_runs is deliberately kept, not dropped: the window scheduler
-- no longer claims slots (it evaluates on every tick, and the enqueue itself is
-- idempotent per segment+target), so nothing writes to it. Leaving the old rows
-- in place costs nothing and keeps the history readable while this change beds
-- in. Dropping it would destroy evidence for a rollback.
