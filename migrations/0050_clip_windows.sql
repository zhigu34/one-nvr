-- Event recordings become windows; events become anchors on them. REC-09.
--
-- The old model had `event_clip_jobs.event_id` as the PRIMARY KEY: one event, at
-- most one clip, and a second event always meant a second file. The detector
-- ends an event when it stops recognising the subject — routinely while the
-- person is still walking out — and starts a new one when it sees them again, so
-- one visit to a driveway arrived as several events and produced several clips
-- with unwatched time between them. Measured on the site: 67 of 162 six-hour
-- activity stretches were split, up to six events each.
--
-- The operator's rule, which this table makes representable: a recording is
-- planned to close at `event end + trail`; an event arriving BEFORE that close
-- moves the close out to its own `end + trail`, repeatedly. The recording is the
-- subject; each event is an anchor on it.
CREATE TABLE clip_windows (
  id uuid PRIMARY KEY,
  channel_id uuid NOT NULL REFERENCES channels(id),
  window_start timestamptz NOT NULL,
  -- Pushed out by each event that arrives before the close. The buffer records
  -- continuously, so extending the window adds real footage rather than
  -- pretending a gap was covered.
  window_end timestamptz NOT NULL,
  state text NOT NULL DEFAULT 'open' CHECK (state IN ('open','finalizing','done','failed')),
  segment_id uuid REFERENCES recording_segments(id) ON DELETE SET NULL,
  clip_run_id uuid REFERENCES recording_runs(id) ON DELETE SET NULL,
  error_code text,
  created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  CHECK (window_end > window_start)
);

-- The attach query: the newest window on this channel that has not closed yet.
CREATE INDEX clip_windows_open ON clip_windows(channel_id, window_end DESC)
  WHERE state IN ('open','finalizing');

-- event_clip_jobs keeps one row per (event), so the detector's repeated messages
-- stay idempotent, but its role is now "which window does this event belong to".
-- The assembly columns stay for the backfill below and for the audit trail; the
-- assembler reads clip_windows instead.
ALTER TABLE event_clip_jobs ADD COLUMN window_id uuid REFERENCES clip_windows(id) ON DELETE CASCADE;
CREATE INDEX event_clip_jobs_window ON event_clip_jobs(window_id);

-- Every existing job becomes its own window, reproducing exactly what the old
-- code produced: historical clips keep their one-event-per-file shape and are not
-- re-cut. A new event arriving on a channel with a still-open historical window
-- extends it, which is the desired behaviour going forward.
--
-- The id is minted in a temporary column first so the window and the job that
-- produced it are paired by construction. Matching on bounds afterwards would
-- attach two events that share a window_start to the same window and lose one.
ALTER TABLE event_clip_jobs ADD COLUMN backfill_window_id uuid;
UPDATE event_clip_jobs SET backfill_window_id = gen_random_uuid() WHERE TRUE;
INSERT INTO clip_windows(id, channel_id, window_start, window_end, state, segment_id, clip_run_id, error_code, created_at, updated_at)
SELECT j.backfill_window_id, j.channel_id, j.window_start, j.window_end,
       CASE
         WHEN j.state = 'done' THEN 'done'
         WHEN j.state = 'failed' THEN 'failed'
         WHEN j.state = 'growing' THEN 'finalizing'
         ELSE 'open'
       END,
       j.segment_id, j.clip_run_id, j.error_code, j.created_at, j.updated_at
FROM event_clip_jobs j;
UPDATE event_clip_jobs SET window_id = backfill_window_id;
ALTER TABLE event_clip_jobs DROP COLUMN backfill_window_id;
-- Every job has a window now, so the link is mandatory: a job without one could
-- never be assembled and would sit in the queue forever.
ALTER TABLE event_clip_jobs ALTER COLUMN window_id SET NOT NULL;

-- Lead and trail become a site setting instead of two pairs of duplicated Go
-- constants (events.PreEventWindow/PostEventWindow and
-- recording.PreEventLead/PostEventTrail both existed, both 5s/10s, with no shared
-- definition).
--
-- Defaults reproduce today's behaviour exactly. They are read when a window is
-- created or extended, so a change applies to the next event; windows already
-- open keep the close they were given.
ALTER TABLE sites
  ADD COLUMN event_clip_lead_seconds integer NOT NULL DEFAULT 5
    CHECK (event_clip_lead_seconds BETWEEN 0 AND 60),
  ADD COLUMN event_clip_trail_seconds integer NOT NULL DEFAULT 10
    CHECK (event_clip_trail_seconds BETWEEN 0 AND 120);
