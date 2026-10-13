-- M3 batch 3: detection zones, business rules and their hits. PRD AI-02/03/04/06.
--
-- Zones are definitions handed to the detector, never evaluated here: the
-- coordinates are normalized against the frame the operator drew on, and
-- Frigate performs the actual zone semantics (inertia, loitering).

CREATE TABLE channel_zones (
  id uuid PRIMARY KEY,
  channel_id uuid NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
  name text NOT NULL CHECK(length(name) BETWEEN 1 AND 32),
  -- Polygons as normalized [x,y] pairs in 0..1, at least three vertices. The
  -- pairwise check keeps a filled shape from being stored; the server also
  -- rejects self-intersection, which PostgreSQL cannot express declaratively.
  coordinates jsonb NOT NULL CHECK(jsonb_typeof(coordinates)='array' AND jsonb_array_length(coordinates)>=3),
  -- Categories this zone cares about. Empty means every tracked category.
  objects jsonb NOT NULL DEFAULT '[]',
  -- The stream geometry the drawing was made against, as observed by the
  -- operator's most recent successful source test. Zero means it was unknown at
  -- drawing time (no successful test yet); a later change in the frame's aspect
  -- ratio means the view moved under the drawing, so the UI asks for
  -- recalibration instead of silently reinterpreting the coordinates.
  reference_width integer NOT NULL DEFAULT 0 CHECK(reference_width>=0),
  reference_height integer NOT NULL DEFAULT 0 CHECK(reference_height>=0),
  version bigint NOT NULL DEFAULT 1 CHECK(version>0),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE(channel_id,name)
);
CREATE INDEX channel_zones_channel ON channel_zones(channel_id);

CREATE TABLE detection_rules (
  id uuid PRIMARY KEY,
  channel_id uuid NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
  name text NOT NULL CHECK(length(name) BETWEEN 1 AND 64),
  -- appear: any detection of the categories; enter: Frigate reports the zone in
  -- entered_zones; dwell: enter plus a minimum duration, which Frigate enforces
  -- as loitering. This library never computes zone geometry itself.
  kind text NOT NULL CHECK(kind IN ('appear','enter','dwell')),
  zone_id uuid REFERENCES channel_zones(id) ON DELETE CASCADE,
  categories jsonb NOT NULL CHECK(jsonb_typeof(categories)='array' AND jsonb_array_length(categories)>=1),
  min_score real NOT NULL DEFAULT 0 CHECK(min_score BETWEEN 0 AND 1),
  -- Arming window in site-local wall clock; a window may cross midnight.
  arm_from text CHECK(arm_from ~ '^[0-2][0-9]:[0-5][0-9]$'),
  arm_to text CHECK(arm_to ~ '^[0-2][0-9]:[0-5][0-9]$'),
  -- Dwell threshold in seconds; required for dwell rules, ignored otherwise.
  dwell_seconds integer CHECK(dwell_seconds BETWEEN 1 AND 86400),
  severity text NOT NULL DEFAULT 'medium' CHECK(severity IN ('low','medium','high')),
  -- Notification cooldown. It limits how often an operator is told, never
  -- whether the event is recorded (AI-05).
  cooldown_seconds integer NOT NULL DEFAULT 60 CHECK(cooldown_seconds BETWEEN 0 AND 86400),
  enabled boolean NOT NULL DEFAULT true,
  version bigint NOT NULL DEFAULT 1 CHECK(version>0),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  -- A zone-typed rule must name a zone, and it must belong to the same channel.
  CHECK((kind='appear' AND zone_id IS NULL) OR (kind IN ('enter','dwell') AND zone_id IS NOT NULL)),
  CHECK((kind='dwell' AND dwell_seconds IS NOT NULL) OR (kind<>'dwell' AND dwell_seconds IS NULL)),
  CHECK((arm_from IS NULL) = (arm_to IS NULL))
);
CREATE INDEX detection_rules_channel ON detection_rules(channel_id);

CREATE TABLE event_rule_hits (
  id uuid PRIMARY KEY,
  event_id uuid NOT NULL REFERENCES events(id) ON DELETE CASCADE,
  rule_id uuid NOT NULL REFERENCES detection_rules(id) ON DELETE CASCADE,
  zone_name text NOT NULL DEFAULT '',
  -- Why this event matched: score, the zone Frigate reported, the wall-clock
  -- window. Stored at match time so a later rule edit cannot rewrite history.
  reason jsonb NOT NULL DEFAULT '{}',
  notified_at timestamptz,
  false_positive boolean NOT NULL DEFAULT false,
  false_positive_note text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  -- AI-05: one upstream target, one rule, one effective zone entry is exactly
  -- one logical hit; a later update of the same event only updates it.
  UNIQUE(event_id,rule_id)
);
CREATE INDEX event_rule_hits_rule ON event_rule_hits(rule_id,created_at DESC);
CREATE INDEX event_rule_hits_open ON event_rule_hits(event_id) WHERE notified_at IS NULL;
