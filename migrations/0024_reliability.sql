-- M4 reliability batch 1: observation history, state intervals and fault events.
-- PRD §6.12 REL-01/02, and the persistence half of REL-06.
--
-- Why new tables instead of the existing ones:
--
-- source_observations is *current state*, not history. Each row expires thirty
-- seconds after it is written and the reader takes only the newest row per
-- channel and stream kind. That makes it the right shape for "is this stream
-- healthy right now" and the wrong shape for "was it healthy for the last
-- month": nothing keeps the older rows, and even the rows we keep say nothing
-- about how long a state lasted.
--
-- recording_gaps is evidence of *recording* interruptions. It is driven by the
-- recorder's own state machine and does not cover the main/sub stream
-- availability PRD REL-01 is about, so it cannot answer "how long was the main
-- stream up".
--
-- So this migration adds the two things the chapter needs and nothing else:
-- intervals derived from observations (the fact layer every rate is a
-- projection of), and fault events derived from interval transitions.

CREATE TABLE reliability_intervals (
  id uuid PRIMARY KEY,
  channel_id uuid NOT NULL REFERENCES channels(id),
  -- The source in effect for this interval. Nullable so an unconfigured or
  -- cleared channel still records its exclusions rather than vanishing.
  source_revision_id uuid,
  session_id uuid,
  kind text NOT NULL CHECK(kind IN ('main','sub','recording')),
  -- healthy/unavailable are the observed states; unknown is "no evidence";
  -- excluded is "not required to be up" and takes its cause from exclusion.
  state text NOT NULL CHECK(state IN ('healthy','unavailable','unknown','excluded')),
  reason_code text NOT NULL,
  -- Why this interval is not counted as an interruption. Null means it is a
  -- real observation. The PRD requires planned stops, unconfigured sources and
  -- registered maintenance to be shown separately instead of blending into
  -- availability, which is exactly what this column carries.
  exclusion text CHECK(exclusion IS NULL OR exclusion IN ('disabled','not_configured','maintenance','cleared')),
  -- Half-open [started_at, ended_at), matching PRD REL-04: intervals never
  -- double-count the instant at a boundary. A null end means still open.
  started_at timestamptz NOT NULL,
  ended_at timestamptz,
  -- Observation counts behind this interval. Two uses: the debounce threshold
  -- reads observed_count, and a reader can tell a one-sample blip from a
  -- sustained state without reading the raw rows.
  observed_count integer NOT NULL DEFAULT 1 CHECK(observed_count>0),
  -- The uncertainty band REQUIRED by REL-01: polling cannot claim to know the
  -- exact instant a stream failed. last_healthy_at is the last observation that
  -- confirmed the previous state; first_unhealthy_at is the first that
  -- confirmed this one. The change happened between them, and a caller that
  -- wants to be honest reports the pair instead of the interval start.
  last_observed_at timestamptz NOT NULL,
  first_observed_at timestamptz NOT NULL,
  created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  CHECK(ended_at IS NULL OR ended_at >= started_at)
);
-- The state at a moment, and the whole history of one channel and stream.
CREATE INDEX reliability_intervals_open ON reliability_intervals(channel_id,kind) WHERE ended_at IS NULL;
CREATE INDEX reliability_intervals_channel_time ON reliability_intervals(channel_id,started_at DESC,id DESC);
CREATE INDEX reliability_intervals_window ON reliability_intervals(started_at,ended_at);
-- Retention sweeps by closed time; open intervals are never swept.
CREATE INDEX reliability_intervals_retention ON reliability_intervals(ended_at) WHERE ended_at IS NOT NULL;

-- One open interval per channel and stream kind. The fold writes through this
-- constraint rather than reading first, so two workers racing cannot open two
-- intervals for the same state.
CREATE UNIQUE INDEX reliability_intervals_one_open ON reliability_intervals(channel_id,kind) WHERE ended_at IS NULL;

CREATE TABLE fault_events (
  id uuid PRIMARY KEY,
  channel_id uuid NOT NULL REFERENCES channels(id),
  source_revision_id uuid,
  session_id uuid,
  -- What failed. Mirrors the interval kind for source faults, and adds the
  -- component-level kinds the PRD enumerates (storage, media server, the
  -- monitor itself, a source switch).
  kind text NOT NULL CHECK(kind IN ('main','sub','recording','storage','media_server','monitoring','switch')),
  state text NOT NULL CHECK(state IN ('open','resolved')),
  started_at timestamptz NOT NULL,
  ended_at timestamptz,
  duration_ms bigint CHECK(duration_ms IS NULL OR duration_ms>=0),
  -- The automatic attribution, written once and never rewritten. PRD REL-02:
  -- an administrator may confirm a cause, but the original classification stays
  -- readable, so a later revision cannot quietly improve the record.
  initial_reason text NOT NULL,
  category text NOT NULL CHECK(category IN ('upstream_unavailable','auth_or_config','recording_service','storage_directory','detection_service','monitoring_unknown','maintenance_or_switch','unknown')),
  -- Administrator confirmation. Null until someone confirms or corrects it.
  confirmed_reason text,
  confirmed_category text CHECK(confirmed_category IS NULL OR confirmed_category IN ('upstream_unavailable','auth_or_config','recording_service','storage_directory','detection_service','monitoring_unknown','maintenance_or_switch','unknown')),
  note text NOT NULL DEFAULT '',
  -- Component-level root event: one media server stopping affects many
  -- channels. Children keep their own rows; only the total business impact is
  -- computed as a union so eight simultaneous faults are not billed as eight
  -- separate outages.
  root_event_id uuid REFERENCES fault_events(id) ON DELETE SET NULL,
  -- Which interval changes and observations justify this fault, so the
  -- classification can be audited rather than trusted.
  evidence jsonb NOT NULL DEFAULT '{}',
  -- Append-only attribution history. PRD: revisions keep their statistics
  -- version and impact, and an administrator cannot erase an unfavourable
  -- record.
  revisions jsonb NOT NULL DEFAULT '[]',
  recovery text CHECK(recovery IS NULL OR recovery IN ('auto','manual')),
  -- True when the fault happened inside a registered maintenance window.
  -- Excluded from unplanned interruption statistics but still recorded.
  maintenance boolean NOT NULL DEFAULT false,
  created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  CHECK(ended_at IS NULL OR ended_at >= started_at)
);
-- One open fault per channel and kind: a repeated observation refreshes the
-- existing fault instead of filing a new one (REL-02 "the same continuous fault
-- updates the original event", and repeated callbacks must be idempotent).
CREATE UNIQUE INDEX fault_events_one_open ON fault_events(channel_id,kind) WHERE state='open';
CREATE INDEX fault_events_channel_time ON fault_events(channel_id,started_at DESC,id DESC);
CREATE INDEX fault_events_open ON fault_events(started_at) WHERE state='open';
CREATE INDEX fault_events_root ON fault_events(root_event_id) WHERE root_event_id IS NOT NULL;
CREATE INDEX fault_events_retention ON fault_events(ended_at) WHERE ended_at IS NOT NULL;

-- Registered maintenance windows. PRD REL-02: planned stops are counted apart
-- from unplanned ones, and a later classification change keeps its own record
-- so nobody can improve the numbers by reclassifying after the fact.
CREATE TABLE maintenance_windows (
  id uuid PRIMARY KEY,
  -- Null means every channel: a site-wide maintenance window.
  channel_id uuid REFERENCES channels(id) ON DELETE CASCADE,
  starts_at timestamptz NOT NULL,
  ends_at timestamptz NOT NULL,
  note text NOT NULL DEFAULT '',
  created_by uuid REFERENCES users(id),
  -- When the row was registered, distinct from when the window starts. A window
  -- registered after the fact is still honoured (the operator may have known
  -- the work was planned) but the registration time is visible, so a report can
  -- tell a forecast from a backfill.
  created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  CHECK(ends_at > starts_at)
);
CREATE INDEX maintenance_windows_time ON maintenance_windows(starts_at,ends_at);

-- The raw observations kept for drill-down need no new index: migration 0023
-- already added source_observations(observed_at), which is exactly what the
-- longer retention sweep filters on. Its thirty-second expires_at column also
-- keeps its meaning — it is what makes a *reader* treat the newest row as stale
-- (source_status depends on that), which is a different question from how long
-- the row is kept. The two were conflated in one column until this batch
-- separated the reader's TTL from the sweeper's window.
