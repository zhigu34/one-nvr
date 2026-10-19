-- A recording can be discarded because its media cannot be decoded.
--
-- The vocabulary so far describes WHO asked for the deletion (manual, retention,
-- quota, watermark). None of them fits removing a file that is unusable: the
-- audio track of an event clip assembled by the pre-fix concatenation path is
-- invalid, so no player can open it, while the index still calls it ready.
-- Recording that as 'manual' would attribute an integrity outcome to an
-- operator's choice.
ALTER TABLE recording_locations DROP CONSTRAINT recording_locations_delete_reason_check;
ALTER TABLE recording_locations ADD CONSTRAINT recording_locations_delete_reason_check
 CHECK (delete_reason IS NULL OR delete_reason IN ('manual','retention','quota','watermark','corrupt'));
