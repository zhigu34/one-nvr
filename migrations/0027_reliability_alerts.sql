-- M4 reliability batch 4: configurable reliability thresholds (PRD REL-05).
--
-- Alerts are deliberately NOT faults, and the separation is the point.
--
-- A fault records something that went wrong, derived from what was observed: the
-- stream was down and the observation says so. An alert records that a threshold
-- an operator configured was crossed. The two differ in exactly the way that
-- matters for history: changing a threshold must change future behaviour and
-- nothing else, so alerts must not be stored in the fault table — a rule edited
-- today would otherwise appear to rewrite what the site recorded last month.
--
-- PRD REL-05 also requires each alert to keep the window it was judged over, the
-- threshold, and the value actually observed. Without those three an alert is
-- just an alarm with no way to check whether it was right.

CREATE TABLE reliability_alert_rules (
  id uuid PRIMARY KEY,
  -- Null means every channel. A site-wide rule is the normal case for "the
  -- monitor stopped" and an odd one for "this camera was down", but the schema
  -- does not prevent either.
  channel_id uuid REFERENCES channels(id) ON DELETE CASCADE,
  kind text NOT NULL CHECK(kind IN (
    'main_down_streak',          -- main stream continuously unavailable
    'recording_segment_timeout', -- no completed segment for too long
    'daily_coverage_drop',       -- a day's recording coverage below a floor
    'monitoring_stopped'         -- no observations at all
  )),
  -- Duration thresholds in milliseconds; percentage thresholds 0-100. One of the
  -- two is used per kind, and the check below keeps a rule from having neither.
  threshold_ms bigint CHECK(threshold_ms IS NULL OR threshold_ms > 0),
  threshold_percent numeric CHECK(threshold_percent IS NULL OR (threshold_percent >= 0 AND threshold_percent <= 100)),
  enabled boolean NOT NULL DEFAULT true,
  version bigint NOT NULL DEFAULT 1,
  updated_by uuid REFERENCES users(id),
  created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  CHECK(threshold_ms IS NOT NULL OR threshold_percent IS NOT NULL),
  -- One rule per channel and kind, so "the threshold for this camera" is a
  -- single row rather than a set a caller has to order.
  UNIQUE(channel_id, kind)
);
-- A site-wide rule is `channel_id IS NULL`, which a unique constraint treats as
-- distinct from itself in PostgreSQL, so it needs its own index to stay single.
CREATE UNIQUE INDEX reliability_alert_rules_site_wide ON reliability_alert_rules(kind) WHERE channel_id IS NULL;

CREATE TABLE reliability_alerts (
  id uuid PRIMARY KEY,
  rule_id uuid NOT NULL REFERENCES reliability_alert_rules(id) ON DELETE CASCADE,
  channel_id uuid NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
  kind text NOT NULL,
  state text NOT NULL CHECK(state IN ('firing','resolved')),
  started_at timestamptz NOT NULL,
  ended_at timestamptz,
  -- The three things REL-05 requires be recorded: the window judged, the
  -- threshold, and the value observed. Kept as numbers rather than text so a
  -- later analysis can compare them.
  window_start timestamptz NOT NULL,
  window_end timestamptz,
  threshold_value numeric NOT NULL,
  observed_value numeric NOT NULL,
  -- Highest value seen while firing, so a resolved alert shows how bad it got
  -- rather than only its final reading.
  peak_value numeric NOT NULL,
  -- When this firing was notified. Set once: a continued condition must not
  -- re-notify every evaluation pass, which is what "deduplicate ongoing faults"
  -- means in practice.
  notified_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  CHECK(ended_at IS NULL OR ended_at >= started_at)
);
-- One firing alert per rule and channel. The evaluator writes through this
-- constraint rather than reading first, so two workers cannot open two alerts
-- for the same condition — the same fence the fault and notification tables use.
CREATE UNIQUE INDEX reliability_alerts_one_firing ON reliability_alerts(rule_id,channel_id) WHERE state='firing';
-- The alert list reads newest first, and retention sweeps by resolution.
CREATE INDEX reliability_alerts_channel_time ON reliability_alerts(channel_id,started_at DESC,id DESC);
CREATE INDEX reliability_alerts_retention ON reliability_alerts(ended_at) WHERE ended_at IS NOT NULL;
