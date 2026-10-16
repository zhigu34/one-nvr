-- Observation cleanup: source_observations rows were never deleted.
--
-- They carry a thirty-second TTL and only the newest row per channel and stream
-- kind is later read, but the writer runs every ten seconds per channel and
-- nothing removed the old rows: at 32 channels that is roughly 25 million rows a
-- month, growing without bound. The table is current state, not history, so the
-- worker now sweeps rows past the retention window.
--
-- The existing index starts with channel_id, so a sweep that filters on
-- observed_at alone cannot use it and would scan the table it is trying to
-- shrink. This index is for the sweep.

CREATE INDEX source_observations_expiry ON source_observations(observed_at);
