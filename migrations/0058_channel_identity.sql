-- Camera identity belongs to the channel, not to a stream revision.
--
-- These three fields describe what the hardware IS. They were stored on
-- source_revisions, which meant editing "海康威视" wrote a new revision — and a
-- new revision is what triggers a re-test and a stream switch. Telling the system
-- that a camera is a Hikvision model X should not interrupt its stream.
--
-- What stays on the revision is what the PROBE observed at that moment; the
-- channel-level value is what the operator asserts. The probe fills the channel
-- value when the operator accepts its answer, so the two do not disagree in
-- practice, and the revision keeps the evidence that produced it.
--
-- Backfill from the current revision: that is the value the UI has been showing,
-- so nothing changes on screen until someone edits it.
ALTER TABLE channels
  ADD COLUMN IF NOT EXISTS brand text NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS model text NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS camera_type text NOT NULL DEFAULT '';

UPDATE channels c
   SET brand = r.brand,
       model = r.model,
       camera_type = r.camera_type
  FROM source_revisions r
 WHERE r.id = c.current_revision_id
   AND (c.brand = '' AND c.model = '' AND c.camera_type = '');
