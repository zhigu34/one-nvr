-- M5 storage batch 1: watermark-driven pressure cleanup and per-channel quotas.
-- PRD STO-02, STO-03, REC-07.
--
-- What already existed: a recorder-start gate that refuses to begin writing
-- when free space falls below max(10GB, ten minutes of measured bitrate for the
-- channels sharing that filesystem). That gate protects the moment a channel
-- *starts*; it does nothing while recordings are already running and the disk
-- keeps filling. A site that never restarts its channels therefore fills the
-- disk and stops recording with no cleanup attempt at all.
--
-- This migration adds the policy the sweeper needs:
--
--   * warning 80%, pressure 85% (clean until back to the target 80%), critical
--     95% (keep cleaning, and surface it as a blocking condition);
--   * an optional per-channel byte quota, independent of the filesystem;
--   * an audit of what the sweeper decided each round, so an operator can see
--     why a recording was deleted instead of finding a hole in the timeline.
--
-- The thresholds live on the pool rather than on the site because the thing
-- being measured is a filesystem, and two pools on different filesystems must
-- be able to run different policies. Pools sharing a filesystem are coordinated
-- at runtime by filesystem_id (already recorded by the capacity probe) rather
-- than by duplicated configuration, because the file that decides this is the
-- same file for both.

ALTER TABLE storage_pools
  ADD COLUMN warning_percent integer NOT NULL DEFAULT 80
    CHECK (warning_percent BETWEEN 1 AND 99),
  ADD COLUMN pressure_percent integer NOT NULL DEFAULT 85
    CHECK (pressure_percent BETWEEN 1 AND 99),
  ADD COLUMN pressure_target_percent integer NOT NULL DEFAULT 80
    CHECK (pressure_target_percent BETWEEN 1 AND 99),
  ADD COLUMN critical_percent integer NOT NULL DEFAULT 95
    CHECK (critical_percent BETWEEN 1 AND 100),
  -- The ordering is what makes the three states meaningful: a "warning" above
  -- the pressure line would leave a band that never fires, and a target above
  -- the pressure line would stop cleaning the instant it started.
  ADD CONSTRAINT storage_pools_watermark_order CHECK (
    warning_percent <= pressure_percent
    AND pressure_target_percent <= pressure_percent
    AND pressure_percent <= critical_percent
  );

-- Quota is per channel, in bytes, 0 = no quota. It is a logical limit applied
-- by the sweeper; it is deliberately not a filesystem quota (STO-03 requires
-- that the two are not conflated).
ALTER TABLE channels
  ADD COLUMN recording_quota_bytes bigint NOT NULL DEFAULT 0
    CHECK (recording_quota_bytes >= 0);

-- One row per pool per sweep decision. This is operational evidence, not the
-- deletion ledger: recording_locations already records each removed file. What
-- this table answers is "at 14:05 the pool was 87% full, so sixteen of the
-- oldest safe segments were removed" — the question an operator asks first.
CREATE TABLE storage_watermark_events (
  id uuid PRIMARY KEY,
  pool_id uuid NOT NULL REFERENCES storage_pools(id),
  -- filesystem_id of the pool at the time of the decision, so two pools on the
  -- same filesystem can be read together after either has been rebound.
  filesystem_id text NOT NULL DEFAULT '',
  -- 'ok' | 'warning' | 'pressure' | 'critical' | 'unknown'. 'unknown' is its own
  -- state: a pool whose capacity has never been observed must not be reported
  -- as healthy just because no threshold was crossed.
  state text NOT NULL CHECK (state IN ('ok','warning','pressure','critical','unknown')),
  used_bytes bigint NOT NULL CHECK (used_bytes >= 0),
  total_bytes bigint NOT NULL CHECK (total_bytes >= 0),
  percent integer NOT NULL CHECK (percent BETWEEN 0 AND 100),
  -- How many segments this round asked the shared delete core to remove.
  requested integer NOT NULL DEFAULT 0 CHECK (requested >= 0),
  deleted integer NOT NULL DEFAULT 0 CHECK (deleted >= 0),
  -- Why the round stopped: 'target_reached', 'no_safe_candidate',
  -- 'batch_exhausted', 'capacity_unknown', 'delete_failed'.
  outcome text NOT NULL DEFAULT '',
  observed_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE INDEX storage_watermark_events_pool_time ON storage_watermark_events(pool_id, observed_at DESC);

-- The sweeper walks the oldest safe candidate per pool on every round. Without
-- this index that walk sorts the whole segment table each time, which on a
-- two-week site is the dominant cost of an already periodic task.
CREATE INDEX recording_segments_pool_age ON recording_segments(pool_id, end_at)
  WHERE state = 'ready' AND NOT protected;

-- Channel quota accounting reads one channel's finished segments by age.
CREATE INDEX recording_segments_channel_age ON recording_segments(channel_id, end_at)
  WHERE state = 'ready' AND NOT protected;

-- Pressure cleanup deletes for a fourth reason. Without this the delete core's
-- audit row would carry a value the location table rejects, which would abort
-- the transaction *after* the file was already unlinked — leaving a recording
-- that is gone from disk but still indexed as ready.
--
-- The existing constraint was added inline in 0013, so its name is
-- server-generated; drop it by discovery instead of guessing.
DO $$
DECLARE constraint_name text;
BEGIN
  SELECT con.conname INTO constraint_name
  FROM pg_constraint con
  JOIN pg_class rel ON rel.oid = con.conrelid
  JOIN pg_attribute att ON att.attrelid = rel.oid AND att.attnum = ANY(con.conkey)
  WHERE rel.relname = 'recording_locations' AND att.attname = 'delete_reason' AND con.contype = 'c'
  LIMIT 1;
  IF constraint_name IS NOT NULL THEN
    EXECUTE format('ALTER TABLE recording_locations DROP CONSTRAINT %I', constraint_name);
  END IF;
END $$;
ALTER TABLE recording_locations ADD CONSTRAINT recording_locations_delete_reason_check
  CHECK (delete_reason IS NULL OR delete_reason IN ('manual','retention','quota','watermark'));


