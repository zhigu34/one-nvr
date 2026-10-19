-- A location can describe a file that is still being written. PRD REC-09.
--
-- recording_locations has only carried the verdicts of settled files
-- (ready/missing/damaged/deleted), because until event clips grew while the event
-- ran, every indexed file was written once and never touched again.
--
-- A running event's clip is now re-cut every few seconds and stays 'finalizing'
-- until the event ends. Its location must carry the same verdict: marking it
-- 'ready' would let the storage reconciler verify a file that is about to be
-- replaced, and mark it damaged on the next pass for having changed — the exact
-- false verdict that made every long event unplayable.
ALTER TABLE recording_locations DROP CONSTRAINT recording_locations_state_check;
ALTER TABLE recording_locations ADD CONSTRAINT recording_locations_state_check
 CHECK(state IN ('ready','finalizing','missing','damaged','deleted'));
