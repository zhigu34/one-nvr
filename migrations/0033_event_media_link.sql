-- M5 recording batch 3: event ↔ recording linkage.
-- PRD REC-09, REC-10.
--
-- media_state has existed since 0018 with the values linked/partial/unavailable
-- and was never written: the column expressed the intent, and nothing computed
-- it. This migration adds the columns that verdict needs, because "linked" on
-- its own is not enough to play anything.
--
-- Why the available span is stored rather than recomputed on read:
--
--  1. Segments are deleted by retention. A read-time join would make an old
--     event silently become "unavailable" once its footage aged out, which reads
--     as "the recording failed" rather than "the footage was deleted on
--     schedule". Storing the verdict lets the UI say which of the two happened.
--  2. The requested window is not the available window. A segment boundary lands
--     wherever the keyframe put it, so the event that asked for 5 seconds of
--     lead-in may have 2. Storing the real span is what lets the page seek
--     accurately instead of overshooting into an empty stretch.

ALTER TABLE events
  -- The playable span, when footage exists. Both null when it does not.
  ADD COLUMN media_start_at timestamptz,
  ADD COLUMN media_end_at timestamptz,
  -- How many segments cover the window. A window split by a source switch is
  -- several files that play as one stretch; the count lets the page say so.
  ADD COLUMN media_segments integer NOT NULL DEFAULT 0 CHECK(media_segments >= 0);

-- media_state gains 'partial': footage covers part of the requested window.
-- Collapsing it into 'linked' would put a play button on a range that ends
-- early, and into 'unavailable' would hide footage that does exist.
ALTER TABLE events DROP CONSTRAINT IF EXISTS events_media_state_check;
ALTER TABLE events ADD CONSTRAINT events_media_state_check
  CHECK (media_state IN ('pending','linked','partial','unavailable'));

-- A span is either absent or ordered. This is the same invariant the segment
-- table enforces, so an event clip can never claim a negative duration.
ALTER TABLE events ADD CONSTRAINT events_media_span_check
  CHECK (media_start_at IS NULL OR media_end_at IS NULL OR media_end_at > media_start_at);

-- Finding events whose footage arrived after the event itself: the reconcile
-- sweep re-evaluates pending rows, and this keeps that sweep cheap on a site
-- with a long event history.
CREATE INDEX events_media_pending ON events(media_state) WHERE media_state = 'pending';
