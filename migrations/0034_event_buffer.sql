-- M5 recording batch 3: event pre-recording buffer.
-- PRD REC-09.
--
-- The buffer is short-lived, in-memory, and almost entirely discarded. Only the
-- window around a detection survives. That shape decides the storage:
--
--   * Its files never enter recording_segments. That table is the index of
--     footage the product promises to keep; a three-second fragment that will be
--     deleted in half a minute is not that, and writing a row per fragment
--     would add tens of thousands of rows a day for data no reader can use.
--   * The buffer's lifetime is a process lifetime. A worker restart empties the
--     tmpfs, which is correct: this is a look-back cache, not a recording. So
--     the state below is a *claim* about what a worker is running, and a claim
--     whose owner is gone must be reclaimed rather than trusted.
--
-- Why a table at all: the scheduler must know which channels are already being
-- buffered so it does not start a second recorder, and an operator must be able
-- to see why a channel has no event footage. Neither can be answered from the
-- filesystem alone, because the filesystem is inside a tmpfs the API cannot see.

CREATE TABLE event_buffers (
  channel_id uuid PRIMARY KEY REFERENCES channels(id),
  -- The run that owns the ZLM recording. Restarting a buffer replaces it, so the
  -- old run must be stopped before the new one starts; recording_runs already
  -- carries the unique path constraint that makes a duplicate impossible.
  run_id uuid NOT NULL,
  stream_session_id uuid NOT NULL,
  source_revision_id uuid NOT NULL,
  -- Where this worker writes fragments. Empty when the deployment reserved no
  -- memory, which is reported as "buffer unavailable", not as "no footage".
  cache_path text NOT NULL,
  -- Seconds of history kept. Chosen from the observed fragment length, not
  -- assumed: ZLM cuts at keyframes, so a 3-second request on a 3.3-second GOP
  -- yields 3.3-second files and the ring must hold enough of them.
  keep_seconds integer NOT NULL CHECK(keep_seconds BETWEEN 5 AND 600),
  started_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  -- The worker that owns this buffer. A buffer claimed by a worker that is no
  -- longer running is stale; the sweep below clears it so the channel is not
  -- permanently "already buffering" with nothing writing.
  owner_token text NOT NULL,
  heartbeat_at timestamptz NOT NULL DEFAULT clock_timestamp()
);

CREATE INDEX event_buffers_stale ON event_buffers(heartbeat_at);

COMMENT ON TABLE event_buffers IS
  'PRD REC-09: which channels a worker is keeping an in-memory pre-event buffer for. Rows are claims by a running worker, not recordings.';

-- A detection that needs footage from the buffer, recorded so the assembly pass
-- has a durable work list. The trigger is the event row itself; this table only
-- tracks the *assembly* so a crash mid-copy does not lose the request.
CREATE TABLE event_clip_jobs (
  event_id uuid PRIMARY KEY REFERENCES events(id) ON DELETE CASCADE,
  channel_id uuid NOT NULL REFERENCES channels(id),
  -- The window to extract, fixed at trigger time: the event may still be
  -- running, and a moving end would keep changing the job.
  window_start timestamptz NOT NULL,
  window_end timestamptz NOT NULL,
  state text NOT NULL DEFAULT 'queued' CHECK(state IN ('queued','running','done','failed')),
  error_code text,
  created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  CHECK(window_end > window_start)
);

-- Only unfinished work is scanned; finished rows stay as the record of what the
-- buffer produced for that event.
CREATE INDEX event_clip_jobs_pending ON event_clip_jobs(created_at) WHERE state IN ('queued','running');

-- recording_runs.purpose gains 'event': the buffer's ZLM recording is a real run
-- (it has a pool-relative work path and must be stopped cleanly), but it is not
-- 'continuous' — it exists to serve detections and is discarded with the cache.
--
-- A separate value rather than reusing 'continuous' because several queries ask
-- "is a continuous recorder running for this pool" to decide whether a pool needs
-- a write proof; an event buffer must not answer yes to that, and conversely the
-- continuous-run checks must not mistake a buffer for ordinary recording.
ALTER TABLE recording_runs DROP CONSTRAINT IF EXISTS recording_runs_purpose_check;
ALTER TABLE recording_runs ADD CONSTRAINT recording_runs_purpose_check
  CHECK (purpose IN ('probe','continuous','event'));
