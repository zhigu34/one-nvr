-- Event clips grow while their event is still running. PRD REC-09.
--
-- The first version assembled a clip once, after the event's window was fixed at
-- ingestion. A detection that lasts five minutes therefore produced no footage
-- for five minutes, and then one file — the page showed "no footage" for the whole
-- time an operator was most likely to be looking at it.
--
-- What changes: while an event runs, each update advances the job's window to
-- "now" and leaves it queued, so the next assembly pass re-cuts the clip from the
-- fragments that have accumulated. The fragments are already on disk, so this is
-- a remux, not a recording.
--
-- Why the segment is reused rather than appended:
--
--   * A growing file cannot be played safely. Appending to an MP4 that has already
--     been served means a viewer can read a half-written moov; re-cutting into a
--     new file and renaming it atomically is the only shape that is safe to watch
--     while it grows.
--   * Reusing one segment row keeps the event pointing at exactly one clip. The
--     alternative — a row per assembly — would leave the event page choosing
--     between forty versions of the same minute.

ALTER TABLE event_clip_jobs
  -- The segment this job produces. Null while nothing has been assembled yet, so
  -- "not built" and "built and later deleted" stay distinguishable.
  ADD COLUMN segment_id uuid REFERENCES recording_segments(id) ON DELETE SET NULL,
  -- The run that owns the clip file. Reused with the segment: a second run would
  -- leave the previous file behind, and the naming rule derives the path from it.
  ADD COLUMN clip_run_id uuid REFERENCES recording_runs(id) ON DELETE SET NULL;

-- 'growing' is a job whose event is still running: the clip exists and is
-- playable, but its window will be extended. Distinct from 'running' (an
-- assembly in progress) and 'queued' (waiting for the next pass).
ALTER TABLE event_clip_jobs DROP CONSTRAINT IF EXISTS event_clip_jobs_state_check;
ALTER TABLE event_clip_jobs ADD CONSTRAINT event_clip_jobs_state_check
  CHECK (state IN ('queued','running','growing','done','failed'));

-- The assembler picks up both queued work and clips that need to grow, ordered so
-- an event that is still running is served before an old finished one.
CREATE INDEX event_clip_jobs_active ON event_clip_jobs(updated_at)
  WHERE state IN ('queued','growing');
