-- M5 recording batch 2: weekly recording schedules.
-- PRD REC-02, REC-04.
--
-- A schedule is a *rule*, not a list of instants: "every weekday from 08:00 to
-- 12:00 and 13:00 to 18:00". Storing the expanded instants would need a job that
-- keeps materialising the future, and every restart or outage would leave gaps
-- in the expansion that look like scheduled downtime. A rule can be evaluated at
-- any instant, including for a window that began before the process started.
--
-- Times are wall-clock times in the *site* timezone, not UTC. A site that
-- schedules 08:00 means 08:00 for the people there, including across a DST
-- transition. Convert only when evaluating.
--
-- The row is keyed by (channel, weekday) so "copy Monday to Tuesday" is a plain
-- insert-or-replace, and a weekday with no rows means "no scheduled recording
-- that day" rather than needing an explicit empty-interval convention.

CREATE TABLE recording_schedules (
  id uuid PRIMARY KEY,
  channel_id uuid NOT NULL REFERENCES channels(id),
  -- 1=Monday .. 7=Sunday, matching ISO-8601 and Go's time.Weekday() + 1.
  -- Using the ISO numbering rather than Go's zero-based Sunday keeps the
  -- database and the UI agreeing without a translation table.
  weekday smallint NOT NULL CHECK(weekday BETWEEN 1 AND 7),
  -- Minutes since local midnight, so a window never straddles a DST jump in a
  -- way that a text time would hide. 0..1439.
  start_minute smallint NOT NULL CHECK(start_minute BETWEEN 0 AND 1439),
  -- An end of exactly 1440 means "until midnight", which is how one contiguous
  -- window is expressed without a special case. It must be greater than start,
  -- so a window is never empty and never negative.
  end_minute smallint NOT NULL CHECK(end_minute BETWEEN 1 AND 1440),
  CHECK(end_minute > start_minute),
  created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE INDEX recording_schedules_channel ON recording_schedules(channel_id, weekday, start_minute);
-- Overlap is rejected by the writer, not by an exclusion constraint: a gist
-- exclusion over these columns needs btree_gist, and a migration that installs
-- an extension requires privileges the application role does not own. The write
-- path checks overlap inside the same transaction that inserts, so the invariant
-- still holds for every writer that goes through the service — including the
-- copy-to-days and copy-to-channels operations, which are the only bulk paths.

COMMENT ON TABLE recording_schedules IS
  'PRD REC-02: weekly windows in site-local wall-clock minutes. Evaluated by the scheduler, never expanded into instants.';
COMMENT ON COLUMN recording_schedules.end_minute IS
  'Exclusive end in minutes since local midnight; 1440 means midnight at the end of the day.';

-- Overnight windows (22:00-02:00) are NOT stored as one row spanning two days:
-- they are two rows, one ending at 1440 on the first day and one starting at 0
-- on the next. That keeps every row comparable against one local date, which is
-- what makes "which window is active now" a single indexed lookup instead of a
-- join across day boundaries. The editor writes both rows from one input; the
-- split exists only in storage.
CREATE TABLE recording_schedule_groups (
  -- Groups the rows of one logical window so the editor can show "22:00-02:00"
  -- as one thing and delete it in one action.
  window_id uuid NOT NULL,
  schedule_id uuid PRIMARY KEY REFERENCES recording_schedules(id) ON DELETE CASCADE
);
CREATE INDEX recording_schedule_groups_window ON recording_schedule_groups(window_id);
