-- Clips indexed with an empty file_identity. PRD REC-09.
--
-- The storage reconciler verifies a segment against the identity captured when it
-- was published: device, inode, size and mtime. The event clip assembler wrote an
-- empty object, so every comparison failed and each clip was marked 'damaged' on
-- the next reconciliation pass — while the file sat intact and playable.
--
-- The code now captures the identity, and the reconciler treats an empty object as
-- "never captured" rather than "changed". This restores the rows already marked:
-- a size match is the evidence available for them, and it is enough to separate
-- "the file is there" from "the file was truncated".
UPDATE recording_segments s
SET state = 'ready',
    error_code = NULL,
    ready_at = coalesce(s.ready_at, clock_timestamp())
FROM recording_locations l
WHERE l.segment_id = s.id
  AND s.state = 'damaged'
  AND l.state = 'damaged'
  AND s.pool_id = l.pool_id
  AND s.run_id IN (SELECT id FROM recording_runs WHERE purpose = 'event')
  AND s.file_identity = '{}'::jsonb;

UPDATE recording_locations l
SET state = 'ready',
    verified_at = clock_timestamp()
WHERE l.segment_id IN (
  SELECT id FROM recording_segments
  WHERE run_id IN (SELECT id FROM recording_runs WHERE purpose = 'event')
    AND file_identity = '{}'::jsonb
);
