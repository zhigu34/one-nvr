-- Event clips with no location row. PRD REC-09 / REC-11.
--
-- The clip assembler only started writing recording_locations after the first
-- fix pass, and 0039 backfilled the rows it could see at that moment: it matched
-- segments whose identity was empty, and these three were already 'ready', so
-- they were skipped. A segment with no location is invisible to the storage
-- reconciler — it is never verified, never counted in pool usage, and its file
-- could disappear without the index noticing.
--
-- The rows are created only where the indexed file is still there, so this
-- cannot invent a location for footage that is gone. The file size is not known
-- to SQL, so size_bytes carries the segment's own recorded size; the reconciler
-- re-verifies both on its next pass and corrects the state if they disagree.
INSERT INTO recording_locations(segment_id, pool_id, relative_path, size_bytes, state, verified_at)
SELECT s.id, s.pool_id, s.target_relative_path, s.size_bytes, 'ready', clock_timestamp()
FROM recording_segments s
JOIN recording_runs r ON r.id = s.run_id
JOIN storage_pools p ON p.id = s.pool_id
WHERE r.purpose = 'event'
  AND s.state = 'ready'
  AND NOT EXISTS (
    SELECT 1 FROM recording_locations l WHERE l.segment_id = s.id AND l.pool_id = s.pool_id
  )
ON CONFLICT(segment_id, pool_id) DO NOTHING;
