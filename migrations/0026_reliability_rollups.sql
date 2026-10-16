-- M4 reliability batch 3: minute and daily rollups (PRD REL-06, second half).
--
-- Batches 1 and 2 established the facts: intervals (was the stream up), faults
-- (what broke and when it recovered) and obligations (was recording required).
-- Those tables answer any question exactly, by re-deriving it from the
-- intervals, which is why every rate so far needed no summary layer.
--
-- That stops working at scale. A 90-day query over 32 channels re-reads tens of
-- thousands of interval rows per channel to produce a handful of numbers. These
-- two tables are the projection that makes long windows cheap.
--
-- Two properties are deliberate and must not be "optimised" away later:
--
--   * They are DERIVED, never authoritative. Every value can be recomputed from
--     the interval tables; nothing that decides anything reads a rollup. REL-06
--     requires that accumulated snapshots never cover for the raw facts, and the
--     only way to honour that is for the rollups to have no authority — a
--     divergence is a bug in the projection, never a new truth.
--   * input_watermark records the newest fact included when the row was
--     computed. A reader crossing from detailed history into rollups can then
--     state what the number is based on instead of implying it saw everything.

CREATE TABLE reliability_minutes (
  channel_id uuid NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
  kind text NOT NULL CHECK(kind IN ('main','sub','recording')),
  -- Aligned to the minute. Half-open [minute, minute+1m), like every interval
  -- in this chapter.
  minute timestamptz NOT NULL,
  -- Observation accounting, from reliability_intervals.
  healthy_ms bigint NOT NULL DEFAULT 0 CHECK(healthy_ms >= 0),
  unavailable_ms bigint NOT NULL DEFAULT 0 CHECK(unavailable_ms >= 0),
  unknown_ms bigint NOT NULL DEFAULT 0 CHECK(unknown_ms >= 0),
  -- Deliberately not required to be up. Shown, never divided by.
  excluded_ms bigint NOT NULL DEFAULT 0 CHECK(excluded_ms >= 0),
  -- Recording accounting, from recording_obligations and recording_segments.
  owed_ms bigint NOT NULL DEFAULT 0 CHECK(owed_ms >= 0),
  covered_ms bigint NOT NULL DEFAULT 0 CHECK(covered_ms >= 0),
  unverified_ms bigint NOT NULL DEFAULT 0 CHECK(unverified_ms >= 0),
  -- Outages overlapping this minute, for the interruption counts.
  fault_count integer NOT NULL DEFAULT 0 CHECK(fault_count >= 0),
  -- When this row was computed, and the newest fact it included. REL-06 wants
  -- both so a stored number can be identified and its staleness judged.
  computed_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  input_watermark timestamptz NOT NULL,
  metric_version integer NOT NULL,
  PRIMARY KEY(channel_id, kind, minute)
);
-- Retention sweeps by age; the primary key leads with channel_id, so a sweep
-- filtered on minute alone cannot use it.
CREATE INDEX reliability_minutes_retention ON reliability_minutes(minute);

CREATE TABLE reliability_daily (
  channel_id uuid NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
  kind text NOT NULL CHECK(kind IN ('main','sub','recording')),
  day date NOT NULL,
  healthy_ms bigint NOT NULL DEFAULT 0 CHECK(healthy_ms >= 0),
  unavailable_ms bigint NOT NULL DEFAULT 0 CHECK(unavailable_ms >= 0),
  unknown_ms bigint NOT NULL DEFAULT 0 CHECK(unknown_ms >= 0),
  excluded_ms bigint NOT NULL DEFAULT 0 CHECK(excluded_ms >= 0),
  owed_ms bigint NOT NULL DEFAULT 0 CHECK(owed_ms >= 0),
  covered_ms bigint NOT NULL DEFAULT 0 CHECK(covered_ms >= 0),
  unverified_ms bigint NOT NULL DEFAULT 0 CHECK(unverified_ms >= 0),
  fault_count integer NOT NULL DEFAULT 0 CHECK(fault_count >= 0),
  -- A severity figure the daily trend needs and a minute row cannot express:
  -- the worst single outage that touched this day, so "which day was worst" is
  -- answerable without walking minute rows.
  longest_fault_ms bigint NOT NULL DEFAULT 0 CHECK(longest_fault_ms >= 0),
  computed_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  input_watermark timestamptz NOT NULL,
  metric_version integer NOT NULL,
  PRIMARY KEY(channel_id, kind, day)
);
CREATE INDEX reliability_daily_retention ON reliability_daily(day);

-- Daily rows are derived from minute rows, so a day may only be finalised once
-- every minute in it is past the minute-retention window. Until then the day is
-- recomputed on each pass, which is why it carries computed_at and a watermark
-- rather than being written once.
--
-- No backfill here, unlike the policy timeline in migration 0025: missing
-- rollups are recomputable from facts that still exist, and manufacturing rows
-- for history the interval tables no longer hold would be fabricating evidence.
-- The rollup loop fills forward, and a reader reports degraded precision for
-- whatever it cannot see.
