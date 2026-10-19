-- Event clips were indexed under their staging path. PRD REC-09.
--
-- The assembler wrote the clip to recordings/CHxx/<date>/CHxx_<time>_<run>.mp4
-- and then recorded the *staging* path (.work/event/<run>/clip.mp4) in the index.
-- It removed the work directory immediately afterwards, so every clip the buffer
-- ever produced read as 'missing' minutes after being assembled: the footage was
-- on disk under its proper name while the gallery said it had been lost.
--
-- The code is fixed. This repairs the rows it already wrote: for each segment
-- whose path points into .work/event, recompute the published path with the same
-- rule FreezePath applies and rewrite both path columns.
--
-- Disabling the immutability trigger is acceptable here and only here: that
-- trigger exists to stop a running system from renaming a published segment,
-- which would break every stored reference. This is a one-time correction of a
-- path that was never correct, and the file it names is the same file. The
-- trigger is re-enabled in the same migration.
ALTER TABLE recording_segments DISABLE TRIGGER recording_publication_immutable;

UPDATE recording_segments s
SET original_relative_path = computed.path,
    target_relative_path = computed.path,
    state = 'ready',
    error_code = NULL,
    ready_at = coalesce(s.ready_at, clock_timestamp())
FROM (
  SELECT s.id,
         'recordings/CH' || lpad(c.channel_no::text, 2, '0')
           || '/' || to_char(s.original_start AT TIME ZONE s.naming_timezone, 'YYYY-MM-DD')
           || '/CH' || lpad(c.channel_no::text, 2, '0')
           || '_' || to_char(s.original_start AT TIME ZONE s.naming_timezone, 'YYYYMMDD_HH24MISS')
           || '_' || replace(s.run_id::text, '-', '') || '.mp4' AS path
  FROM recording_segments s
  JOIN channels c ON c.id = s.channel_id
  WHERE s.original_relative_path LIKE '.work/event/%'
) AS computed
WHERE s.id = computed.id;

ALTER TABLE recording_segments ENABLE TRIGGER recording_publication_immutable;
