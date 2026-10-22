-- EVT-03 (the open/acknowledged/closed/false_positive worklist) is withdrawn by
-- the site owner: an event is evidence to watch, not a ticket to triage. What
-- an operator needs on the card is whether footage exists, and the way to see it
-- is the recording itself.
--
-- The columns and the trail table go with it. `events.version` existed only to
-- guard those transitions (Handle was its single writer), so a version that can
-- never change would suggest a concurrency guarantee that no longer exists.
--
-- Verified on the site before applying: event_handling held 0 rows, every
-- handle_state was 'open', every handle_note was empty. Nothing recorded is
-- lost; the event index, rule hits and their own notes are untouched.
DROP TABLE IF EXISTS event_handling;

ALTER TABLE events DROP COLUMN IF EXISTS handle_state;
ALTER TABLE events DROP COLUMN IF EXISTS handle_note;
ALTER TABLE events DROP COLUMN IF EXISTS version;
