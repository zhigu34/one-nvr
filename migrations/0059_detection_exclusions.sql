-- Detection exclusions: time-of-day windows in which a region's detections are
-- not reported.
--
-- The problem this exists for: at night an IR-illuminated parked scooter reads
-- as a person at 0.77 confidence, over and over, because the classifer has never
-- seen that shape under infrared. Raising min_score globally hides real people on
-- other cameras, and a static mask hides them all day.
--
-- What this is NOT: a way to stop detecting. The detector keeps running and the
-- event is still recorded — with `excluded_reason` set — so an operator can audit
-- how much a rule is suppressing. An exclusion that silently deleted rows would
-- make a too-wide rule indistinguishable from a working one.
--
-- The region is a polygon in normalized frame coordinates, the same form and
-- reference-frame discipline as channel_zones. It is stored here rather than
-- reusing channel_zones because an exclusion answers a different question
-- ("where should we stay quiet") than a zone ("where do we look"), and coupling
-- them would make deleting one break the other.
CREATE TABLE detection_exclusions (
  id uuid PRIMARY KEY,
  channel_id uuid NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
  name text NOT NULL CHECK(length(name) BETWEEN 1 AND 64),
  -- Normalized [x,y] pairs, at least three vertices, same shape as zones so the
  -- existing polygon editor and its validation rules apply unchanged.
  coordinates jsonb NOT NULL CHECK(jsonb_typeof(coordinates)='array' AND jsonb_array_length(coordinates)>=3),
  -- Categories this exclusion covers. Empty means every category, which is the
  -- common case ("nothing here counts at night").
  categories jsonb NOT NULL DEFAULT '[]',
  -- Site-local wall clock, same convention as detection_rules.arm_from/arm_to:
  -- a window whose end is not after its start crosses midnight. NULL/NULL means
  -- always — a plain static mask.
  from_clock text CHECK(from_clock ~ '^[0-2][0-9]:[0-5][0-9]$'),
  to_clock text CHECK(to_clock ~ '^[0-2][0-9]:[0-5][0-9]$'),
  -- What to tell the reader of an excluded event.
  reason text NOT NULL DEFAULT '时段内区域排除' CHECK(length(reason) BETWEEN 1 AND 64),
  enabled boolean NOT NULL DEFAULT true,
  version bigint NOT NULL DEFAULT 1 CHECK(version>0),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CHECK((from_clock IS NULL) = (to_clock IS NULL)),
  UNIQUE(channel_id,name)
);
CREATE INDEX detection_exclusions_channel ON detection_exclusions(channel_id) WHERE enabled;

-- Why a row was excluded. NULL means it was not: the column is the audit trail,
-- so "excluded" is a state of the same row rather than a separate table that
-- would have to be joined and kept in step.
ALTER TABLE events ADD COLUMN excluded_reason text
  CHECK(excluded_reason IS NULL OR length(excluded_reason) BETWEEN 1 AND 64);
ALTER TABLE events ADD COLUMN excluded_rule_id uuid REFERENCES detection_exclusions(id) ON DELETE SET NULL;
-- The events list filters these out, so the index has to agree with that filter.
CREATE INDEX events_visible ON events(started_at DESC, id DESC) WHERE excluded_reason IS NULL;
