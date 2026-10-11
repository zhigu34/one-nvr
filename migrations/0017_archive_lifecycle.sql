-- Cloud archive batch 3: playback leases, local safe-cleanup window and the
-- cloud retention deadline. PRD ARC-04/05/09.

-- A live lease keeps an in-flight playback alive against cleanup; it is
-- registered by the content endpoint and renewed by the player heartbeat.
CREATE TABLE recording_playback_leases (
  segment_id uuid PRIMARY KEY REFERENCES recording_segments(id) ON DELETE CASCADE,
  lease_expires_at timestamptz NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);

-- Batch 1 shipped the copies state check without the expired terminal state;
-- the expiry sweep (ARC-09) needs it.
ALTER TABLE recording_archive_copies DROP CONSTRAINT recording_archive_copies_state_check;
ALTER TABLE recording_archive_copies ADD CONSTRAINT recording_archive_copies_state_check
  CHECK(state IN ('uploading','verified','failed','expired'));

-- ARC-09: the cloud deadline derives from the segment's real end time, never
-- from the upload moment. Backfilled here for copies that already exist.
ALTER TABLE recording_archive_copies ADD COLUMN cloud_expires_at timestamptz;
UPDATE recording_archive_copies c SET cloud_expires_at = s.end_at + make_interval(days=>t.cloud_retention_days)
  FROM recording_segments s, archive_targets t WHERE c.segment_id=s.id AND c.target_id=t.id;
CREATE INDEX recording_archive_copies_expiry ON recording_archive_copies(cloud_expires_at) WHERE state='verified';

-- ARC-05: channels bound to an archive policy keep their local source for this
-- many hours past the segment's real end (0 = never auto-clean); the plain
-- days-based retention keeps governing unbound channels.
ALTER TABLE sites ADD COLUMN archive_local_retention_hours integer NOT NULL DEFAULT 48 CHECK(archive_local_retention_hours>=0);
