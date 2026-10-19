-- Clips whose identity predates the re-cut refresh. PRD REC-09.
--
-- Before the re-cut path refreshed file_identity, every reassembly left the
-- segment describing the previous file: the size had grown and the rename had
-- given it a new inode. The rows still carry those stale values, so the
-- reconciler compares a new file against old evidence and quarantines it.
--
-- The identity cannot be recomputed in SQL — it lives in the file's inode — so
-- these rows are reset to "never captured". That is honest: the evidence on these
-- rows is not evidence about the current file. The size comparison still runs,
-- which is what separates "the file is there" from "the file was truncated", and
-- the next re-cut writes a correct identity.
--
-- Scoped to event clips whose recorded size disagrees with the file they index:
-- a continuous segment whose identity matches is untouched.
UPDATE recording_segments s
SET file_identity = '{}'::jsonb,
    state = 'ready',
    error_code = NULL,
    ready_at = coalesce(s.ready_at, clock_timestamp())
FROM recording_locations l
WHERE l.segment_id = s.id
  AND l.pool_id = s.pool_id
  AND s.run_id IN (SELECT id FROM recording_runs WHERE purpose = 'event')
  AND s.state = 'damaged'
  AND s.file_identity <> '{}'::jsonb
  AND (s.file_identity->>'Size')::bigint <> s.size_bytes;

UPDATE recording_locations l
SET state = 'ready',
    verified_at = clock_timestamp()
FROM recording_segments s
WHERE s.id = l.segment_id
  AND s.pool_id = l.pool_id
  AND l.state = 'damaged'
  AND s.state = 'ready'
  AND s.run_id IN (SELECT id FROM recording_runs WHERE purpose = 'event');
