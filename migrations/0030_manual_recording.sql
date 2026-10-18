-- M5 recording batch 1: manual recording windows and the union scheduler contract.
-- PRD REC-03, REC-04.
--
-- Why a table and not a mode value:
--
-- PRD REC-04 says the scheduler computes whether to record from the *union* of
-- continuous, planned and manual state. Those are not mutually exclusive: a
-- channel set to continuous that an operator manually starts and later lets
-- expire must keep recording, and a manual recording must not be cancelled by a
-- policy edit that happens while it runs.
--
-- recording_policies.mode already carries 'none' | 'continuous' | 'planned' —
-- mutually exclusive by construction, one row per channel. Manual recording is
-- different in kind: it is an *event with a start and an end*, not a setting, and
-- "the operator pressed record for 30 minutes" is history that must not be
-- overwritten by the next thing they press. A separate table keeps the setting
-- and the window from competing for one column.
CREATE TABLE recording_manual_windows (
  id uuid PRIMARY KEY,
  channel_id uuid NOT NULL REFERENCES channels(id),
  -- Set by the operator, in minutes. The PRD bounds it 1..120 with a 30 default;
  -- the check repeats the bound so a hand-written INSERT cannot exceed it.
  requested_minutes integer NOT NULL CHECK (requested_minutes BETWEEN 1 AND 120),
  started_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  -- The moment the window stops asking to record. Computed at insert from
  -- started_at + requested_minutes so a later change to the default cannot
  -- lengthen a window that is already running.
  expires_at timestamptz NOT NULL,
  -- Who started it, for the audit trail the product already keeps elsewhere.
  actor_id uuid,
  -- Set when the window is no longer active, with why:
  --   'expired'   — the deadline passed (the normal end)
  --   'cancelled' — an operator stopped it early
  -- NULL while active. Cancelling keeps the row: "recorded manually from 10:00
  -- to 10:12, stopped early" is history, and deleting it would leave the
  -- recording it produced unexplained.
  ended_at timestamptz,
  end_reason text CHECK (end_reason IS NULL OR end_reason IN ('expired','cancelled')),
  CHECK (expires_at > started_at),
  CHECK ((ended_at IS NULL) = (end_reason IS NULL))
);

-- At most one active window per channel: pressing record twice replaces the
-- deadline rather than stacking two windows that would both have to expire.
CREATE UNIQUE INDEX recording_manual_windows_one_active
  ON recording_manual_windows(channel_id) WHERE ended_at IS NULL;

-- The scheduler asks "is this channel inside an active window right now" once
-- per channel per tick, and the sweep asks "which windows expired" on its own
-- cadence. Both walk this index.
CREATE INDEX recording_manual_windows_live
  ON recording_manual_windows(expires_at) WHERE ended_at IS NULL;

-- The union rule the scheduler follows, stated once where both the code and a
-- future reader can find it: a channel records when its policy mode is
-- continuous, OR the schedule says so (batch 2), OR an active manual window
-- exists. 'none' is the absence of every one of those, not a mode that cancels
-- the others.
COMMENT ON TABLE recording_manual_windows IS
  'PRD REC-03/04: a manual recording is a bounded window, not a mode. The scheduler records when continuous OR scheduled OR an active window exists.';
