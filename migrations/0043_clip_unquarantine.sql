-- Un-quarantine event clips the earlier repairs left behind. PRD REC-09.
--
-- Four repair migrations ran in sequence while the cause was still being
-- understood, and each was written against the state the previous one left. These
-- rows ended up matching none of them: their identity is already "never captured"
-- (a later migration reset it) but their state is still damaged, and every
-- earlier condition required either a populated identity or a state that had
-- already been cleared.
--
-- The condition here is the data as it is now, not as any earlier pass expected
-- it: an event clip marked damaged whose recorded size matches the location. The
-- size match is the evidence available, and it is what separates a file that is
-- present from one that was truncated.
UPDATE recording_segments s
SET state = 'ready',
    error_code = NULL,
    ready_at = coalesce(s.ready_at, clock_timestamp())
FROM recording_locations l
WHERE l.segment_id = s.id
  AND l.pool_id = s.pool_id
  AND s.state = 'damaged'
  AND s.file_identity = '{}'::jsonb
  AND s.size_bytes = l.size_bytes
  AND s.run_id IN (SELECT id FROM recording_runs WHERE purpose = 'event');

UPDATE recording_locations l
SET state = 'ready',
    verified_at = clock_timestamp()
FROM recording_segments s
WHERE s.id = l.segment_id
  AND s.pool_id = l.pool_id
  AND l.state = 'damaged'
  AND s.state = 'ready'
  AND s.run_id IN (SELECT id FROM recording_runs WHERE purpose = 'event');
