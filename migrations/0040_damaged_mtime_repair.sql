-- Restore segments quarantined only because their mtime moved. PRD REC-11.
--
-- The storage reconciler compared the file's mtime against the value captured at
-- publication and treated any difference as damage. Copying a file, restoring it
-- from a backup, or anything that rewrites metadata moves the mtime without
-- touching a byte of footage — measured on the live site, 45 segments were
-- quarantined while all 45 probed clean and played normally.
--
-- The reconciler no longer uses mtime for that verdict: identity is device,
-- inode and size, which is what actually separates "the same file" from
-- "replaced or truncated". This restores the rows already marked.
UPDATE recording_segments s
SET state = 'ready',
    error_code = NULL,
    ready_at = coalesce(s.ready_at, clock_timestamp())
FROM recording_locations l
WHERE l.segment_id = s.id
  AND l.pool_id = s.pool_id
  AND s.state = 'damaged'
  AND l.state = 'damaged'
  AND s.file_identity <> '{}'::jsonb
  AND s.size_bytes = l.size_bytes;

UPDATE recording_locations l
SET state = 'ready',
    verified_at = clock_timestamp()
FROM recording_segments s
WHERE s.id = l.segment_id
  AND s.pool_id = l.pool_id
  AND l.state = 'damaged'
  AND s.state = 'ready'
  AND s.file_identity <> '{}'::jsonb;
