-- The cloud deadline was never written, so it never expired anything. ARC-09.
--
-- `archive_targets.cloud_retention_days` has been configurable since 0015 and the
-- archive target page has shown it as "云端保留 / 默认 365 天" the whole time. But
-- `recording_archive_copies.cloud_expires_at` was only ever filled by 0017's
-- one-off backfill: every copy uploaded after that migration carried NULL.
--
-- The expiry sweep selects `cloud_expires_at < clock_timestamp()`, and NULL
-- compares as unknown rather than true, so no copy was ever due. Measured before
-- this fix: 1049 verified copies, zero with a deadline, and setting the target to
-- 30 days would still have kept every object forever.
--
-- The code now stamps the deadline when a copy is created. This backfills the
-- rows that predate that, using the same rule: the segment's real end plus the
-- target's retention. 0 days stays NULL, which everywhere in this codebase means
-- "keep forever" — that is the documented meaning of the field, not an oversight.
UPDATE recording_archive_copies c
SET cloud_expires_at = s.end_at + make_interval(days => t.cloud_retention_days)
FROM recording_segments s, archive_targets t
WHERE c.segment_id = s.id
  AND c.target_id = t.id
  AND c.state = 'verified'
  AND c.cloud_expires_at IS NULL
  AND t.cloud_retention_days > 0;

COMMENT ON COLUMN recording_archive_copies.cloud_expires_at IS
  'ARC-09: when the remote object becomes deletable. Derived from the segment end, never the upload moment; NULL means keep forever (0 days).';

-- The target form has said "默认 365 天，填 0 为永久保留" since the page was
-- built, but 0015 constrained this column to > 0 and the service rejected 0 as
-- invalid input: the promise could never be kept, because the form could not be
-- submitted with 0 at all. 0 means NULL deadline, which every reader in the
-- codebase already treats as "no expiry" — that is the documented meaning, so
-- the constraint is what was wrong.
ALTER TABLE archive_targets DROP CONSTRAINT archive_targets_cloud_retention_days_check;
ALTER TABLE archive_targets ADD CONSTRAINT archive_targets_cloud_retention_days_check
  CHECK (cloud_retention_days BETWEEN 0 AND 36500);
