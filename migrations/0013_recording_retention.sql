-- M2 recording lifecycle: retention, protection and local deletion.
--
-- Three additions, all forward-compatible with the M1-B/M1-C contract:
--
--  1. Retention is opt-in. The column defaults to 0 (disabled), so an upgraded
--     site keeps the M1 behaviour of never deleting a published recording until
--     an administrator explicitly sets a retention period. Retaining M1's
--     "no automatic deletion" default is deliberate: automatic deletion of
--     existing footage is not something an upgrade may decide on the operator's
--     behalf.
--  2. A recording that was deliberately deleted is 'deleted', distinct from
--     'missing' (a file that vanished outside one-nvr). PRD FILE-04 requires the
--     two never be confused, and the crash window between unlinking a file and
--     recording the outcome must stay visible: an interrupted delete leaves the
--     row 'ready' and the existing reconciliation marks it 'missing'.
--  3. 'protected' is evidence preservation: retention and operator deletion both
--     skip it. Without it, a retention period would silently destroy the
--     footage an operator meant to keep.
ALTER TABLE sites ADD COLUMN recording_retention_days integer NOT NULL DEFAULT 0
 CHECK(recording_retention_days BETWEEN 0 AND 3650);

ALTER TABLE recording_segments ADD COLUMN protected boolean NOT NULL DEFAULT false;
ALTER TABLE recording_segments ADD COLUMN protected_at timestamptz;
ALTER TABLE recording_segments DROP CONSTRAINT recording_segments_state_check;
ALTER TABLE recording_segments ADD CONSTRAINT recording_segments_state_check
 CHECK(state IN ('finalizing','ready','missing','damaged','conflict','provisional','deleted'));

ALTER TABLE recording_locations ADD COLUMN deleted_at timestamptz;
ALTER TABLE recording_locations ADD COLUMN delete_reason text
 CHECK(delete_reason IS NULL OR delete_reason IN ('manual','retention'));
ALTER TABLE recording_locations DROP CONSTRAINT recording_locations_state_check;
ALTER TABLE recording_locations ADD CONSTRAINT recording_locations_state_check
 CHECK(state IN ('ready','missing','damaged','deleted'));

-- Retention scans candidates by age; partial indexes keep that scan off the
-- published-and-kept majority of the table.
CREATE INDEX recording_retention_scan ON recording_segments(end_at)
 WHERE state='ready' AND NOT protected;
CREATE INDEX recording_protected ON recording_segments(channel_id,start_at DESC)
 WHERE protected;
