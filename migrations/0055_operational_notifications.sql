-- Operational notifications: which channel kinds hear about which faults.
--
-- Until now the only thing that produced a notification was a detection rule
-- hit. Every operational fault — a camera offline, recording stalled, a pool
-- refusing writes, the detector down — was recorded in abnormal_events and shown
-- on the operations page, and told nobody. The failure mode that matters: a
-- camera dies on Friday, the page has the row, and nobody looks until the footage
-- is needed.
--
-- Why a separate subscription rather than another detection rule: a detection rule
-- answers "what did the camera see". These answer "is the system healthy", and
-- they have no camera, no zone and no category. Hanging them off detection_rules
-- would mean inventing a placeholder rule per kind and giving the events page
-- rows about a camera that saw nothing.
--
-- Why per-kind rows rather than one "send everything" switch: an operator wants
-- a camera offline in the duty group at 3am, and probably does not want a
-- queue-backlog notice there. The kinds are the granularity the operator thinks
-- in.

CREATE TABLE operational_notification_subscriptions (
  id uuid PRIMARY KEY,
  kind text NOT NULL,
  -- The delivery channel. Same table the detection notifications use, so one
  -- WeCom group or one mailbox serves both without being configured twice.
  channel_id uuid NOT NULL REFERENCES notification_channels(id) ON DELETE CASCADE,
  -- Minimum severity to send. A subscription at 'warning' covers warning and
  -- critical; 'info' covers everything. Stored rather than inferred so lowering
  -- the bar for one kind does not require editing the fault classification.
  min_severity text NOT NULL DEFAULT 'info'
    CHECK (min_severity IN ('critical','warning','info')),
  enabled boolean NOT NULL DEFAULT true,
  created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  -- One row per kind per destination: two subscriptions to the same channel for
  -- the same kind would send every alert twice.
  UNIQUE (kind, channel_id)
);
CREATE INDEX operational_subscriptions_channel ON operational_notification_subscriptions(channel_id);

-- What has already been told, so "start and recovery only" is a fact in the
-- database rather than a promise in the code.
--
-- abnormal_events already guarantees one firing row per (kind, subject). This
-- column records that the OPENING of that row was announced, and resolved_notified
-- that its closure was. A sweep that re-reads the same firing row every ten
-- seconds must not send again, and a restart must not re-send an alert the
-- operator already got.
ALTER TABLE abnormal_events ADD COLUMN announced_at timestamptz;
ALTER TABLE abnormal_events ADD COLUMN resolved_announced_at timestamptz;
CREATE INDEX abnormal_events_unannounced ON abnormal_events(kind)
  WHERE state = 'firing' AND announced_at IS NULL;
CREATE INDEX abnormal_events_unannounced_resolution ON abnormal_events(kind)
  WHERE state = 'resolved' AND resolved_announced_at IS NULL;
