-- Restore event clips that were quarantined for an empty file identity.
-- PRD REC-09.
--
-- 0038 tried to do this by joining recording_locations, which does not work for
-- these rows: the clip assembler never wrote a location at all, so the join
-- matched nothing and nothing was repaired. A second defect hiding behind the
-- first — the location is what the storage reconciler walks, so the clips were
-- also invisible to every verification pass.
--
-- This does two things: restores the segments (an empty identity is "never
-- captured", and the file is intact), and creates the missing location rows so
-- the reconciler can see and verify them from now on.
INSERT INTO recording_locations(segment_id, pool_id, relative_path, size_bytes, state, verified_at)
SELECT s.id, s.pool_id, s.target_relative_path, s.size_bytes, 'ready', clock_timestamp()
FROM recording_segments s
JOIN recording_runs r ON r.id = s.run_id
WHERE r.purpose = 'event'
  AND s.file_identity = '{}'::jsonb
ON CONFLICT(segment_id, pool_id) DO UPDATE
SET relative_path = EXCLUDED.relative_path,
    size_bytes = EXCLUDED.size_bytes,
    state = 'ready',
    verified_at = clock_timestamp();

UPDATE recording_segments s
SET state = 'ready',
    error_code = NULL,
    ready_at = coalesce(s.ready_at, clock_timestamp())
FROM recording_runs r
WHERE r.id = s.run_id
  AND r.purpose = 'event'
  AND s.state = 'damaged'
  AND s.file_identity = '{}'::jsonb;
