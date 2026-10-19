-- The last event clips still quarantined for an empty identity.
--
-- 0039 restored clips whose paths had already been repaired and created their
-- missing location rows. A handful escaped because their state was 'ready' when
-- 0039 ran (0037 had just restored the path) and a reconciliation pass marked
-- them damaged afterwards, once the reconciler saw the empty identity.
--
-- The order of two repairs is what let them slip through, so this pass is stated
-- in the opposite order: quarantine first, then restore, in one statement whose
-- condition is the data rather than the sequence.
UPDATE recording_locations l
SET state = 'ready',
    verified_at = clock_timestamp()
FROM recording_segments s
WHERE s.id = l.segment_id
  AND s.pool_id = l.pool_id
  AND s.state = 'damaged'
  AND s.file_identity = '{}'::jsonb
  AND s.size_bytes = l.size_bytes
  AND s.run_id IN (SELECT id FROM recording_runs WHERE purpose = 'event');

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
