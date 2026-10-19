-- M5 events batch: abnormal (operational) events. PRD EVT-04.
--
-- Why a second table rather than more `reliability_alerts` kinds:
--
--   * That table is per-channel by construction (`channel_id NOT NULL`) because
--     REL-05 asks "which camera is worse than the others". Half of EVT-04 is not
--     about a camera at all: the detector being unreachable, the media server
--     being unreachable, a config publish failing. Forcing those onto a channel
--     would mean inventing an arbitrary one and then answering a question nobody
--     asked.
--   * The two also answer different questions. An alert says a threshold was
--     crossed and keeps its peak. An incident says what is broken right now, on
--     which component, and what the operator should look at — and it is expected
--     to be read as a worklist, ordered by severity rather than by channel.
--
-- What is shared with alerts: the dedup fence. One open incident per (source,
-- subject) means a condition that persists updates one row instead of writing a
-- row per evaluation pass, which is EVT-04's explicit requirement.

CREATE TABLE abnormal_events (
  id uuid PRIMARY KEY,
  -- kind is the EVT-04 category. Kept as text with a CHECK rather than an enum
  -- so a new category is a migration, not a client release.
  kind text NOT NULL CHECK(kind IN (
    'camera_offline',       -- a channel's source is unreachable
    'stream_degraded',      -- main/sub abnormal while the camera is up
    'recording_stalled',    -- stream online but segments stopped landing
    'storage_unwritable',   -- a pool cannot be written
    'capacity_low',         -- free space below the working line
    'detector_unavailable', -- Frigate unreachable or not answering
    'broker_unavailable',   -- MQTT unreachable
    'config_publish_failed',-- detection configuration could not be applied
    'queue_backlog'         -- detection work is falling behind
  )),
  -- severity orders the worklist. A camera being offline is less urgent than the
  -- detector being offline, which blinds every camera at once.
  severity text NOT NULL CHECK(severity IN ('critical','warning','info')),
  -- subject identifies the thing that is broken: a channel id, a pool id, or a
  -- fixed component name. Text rather than a nullable set of foreign keys because
  -- the kinds have different subject types and a wrong join would silently show
  -- an incident against no channel.
  subject text NOT NULL,
  -- The channel this concerns, when it concerns one. Null is meaningful: a
  -- site-wide condition (detector down) is not attached to a camera, and
  -- reporting it as "all cameras" would inflate every channel's history.
  channel_id uuid REFERENCES channels(id) ON DELETE CASCADE,
  state text NOT NULL CHECK(state IN ('firing','resolved')),
  -- What the operator reads. Kept as the observed detail at open time so a later
  -- change of wording in the code does not rewrite history.
  summary text NOT NULL,
  detail text NOT NULL DEFAULT '',
  -- Counted, not listed: a condition re-evaluated every ten seconds for an hour
  -- would otherwise produce six thousand evidence rows to say one thing.
  occurrences integer NOT NULL DEFAULT 1 CHECK(occurrences > 0),
  started_at timestamptz NOT NULL,
  last_seen_at timestamptz NOT NULL,
  ended_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  CHECK((state='firing') = (ended_at IS NULL)),
  CHECK(ended_at IS NULL OR ended_at >= started_at)
);

-- The dedup fence. One open incident per broken thing: a persistent fault updates
-- its row (occurrences, last_seen_at) rather than opening a second one.
CREATE UNIQUE INDEX abnormal_events_one_open ON abnormal_events(kind,subject) WHERE state='firing';
-- The worklist reads firing first, most severe first.
CREATE INDEX abnormal_events_worklist ON abnormal_events(state,severity,last_seen_at DESC);
CREATE INDEX abnormal_events_channel ON abnormal_events(channel_id,started_at DESC) WHERE channel_id IS NOT NULL;
-- Retention sweeps resolved rows by age; open ones are never old news.
CREATE INDEX abnormal_events_retention ON abnormal_events(ended_at) WHERE ended_at IS NOT NULL;

COMMENT ON TABLE abnormal_events IS
  'PRD EVT-04: operational conditions the product should report without being asked. One open row per (kind,subject); resolution keeps the history.';
