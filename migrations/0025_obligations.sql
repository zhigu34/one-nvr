-- M4 reliability batch 2: recording obligation intervals and their evidence.
-- PRD §6.12 REL-03.
--
-- Batch 1 recorded whether a stream was up. This one records whether recording
-- was *required* — the denominator every completeness figure is divided by.
--
-- Why the denominator cannot be derived from the current policy: PRD REL-03
-- forbids reinterpreting the past with today's plan. recording_policies holds
-- one row per channel with the mode that is true right now, so applying it to
-- last month would silently rewrite history every time an operator changes a
-- setting. The first table below is the missing timeline.

CREATE TABLE recording_policy_versions (
  id uuid PRIMARY KEY,
  channel_id uuid NOT NULL REFERENCES channels(id),
  -- Mirrors recording_policies.version at the moment this row was written, so
  -- an obligation can name the exact policy it was derived from.
  version bigint NOT NULL CHECK(version > 0),
  mode text NOT NULL CHECK(mode IN ('none','continuous','planned')),
  event_recording boolean NOT NULL DEFAULT false,
  -- When this version took effect, which is not the same as when the row was
  -- written: the write happens inside the switch transaction, while
  -- effective_at is the instant the change became the truth. Half-open
  -- [effective_at, next version's effective_at), matching REL-04.
  effective_at timestamptz NOT NULL,
  -- Why the version changed. 'backfill' is the honest label for rows this
  -- migration had to invent for policies that predate the table.
  reason text NOT NULL CHECK(reason IN ('setup','policy_apply','source_switch','rollback','backfill')),
  actor_id uuid REFERENCES users(id),
  switch_id uuid REFERENCES source_switches(id),
  created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  -- One row per policy version, so a retried switch cannot double-write and a
  -- reader joining obligations to policies always finds exactly one match.
  UNIQUE(channel_id, version)
);
CREATE INDEX recording_policy_versions_time ON recording_policy_versions(channel_id,effective_at DESC,id DESC);

CREATE TABLE recording_obligations (
  id uuid PRIMARY KEY,
  channel_id uuid NOT NULL REFERENCES channels(id),
  -- The source in effect for this stretch. REL-03 requires the obligation to
  -- name source_id and source version, so a later camera swap does not
  -- retroactively change what was owed.
  source_revision_id uuid,
  session_id uuid,
  -- Which policy version formed this obligation. Kept alongside (rather than
  -- only jointed through) because the join needs a time comparison and this
  -- makes the derivation auditable even if a timeline row is later swept.
  policy_version bigint NOT NULL,
  -- Where the requirement comes from. 'planned' and 'manual' are reserved
  -- vocabulary: PRD REC-02/03/04 are unimplemented, so nothing writes them
  -- today, and the completeness reader reports them as unsupported rather than
  -- pretending they are covered.
  source text NOT NULL CHECK(source IN ('continuous','planned','manual')),
  -- Why recording was NOT required. Null means it was. Same vocabulary as
  -- reliability_intervals.exclusion so the two tables can be read together.
  -- PRD REL-03: deliberate stops, unconfigured sources and registered
  -- maintenance are each shown separately instead of blending into the totals.
  exclusion text CHECK(exclusion IS NULL OR exclusion IN ('disabled','not_configured','maintenance','cleared')),
  -- Half-open [started_at, ended_at): a boundary instant belongs to exactly one
  -- obligation, which is what keeps the union from double-counting it.
  started_at timestamptz NOT NULL,
  ended_at timestamptz,
  -- When the reconciler last confirmed this verdict. Two uses: closing a
  -- changed obligation at the last instant that still supported the old verdict
  -- (rather than at the sample that noticed the change, which would credit the
  -- new verdict with time the old one governed), and letting a reader see that
  -- the reconciler was still alive without inspecting raw rows.
  last_observed_at timestamptz NOT NULL,
  -- What the derivation looked at: the source switch covering this stretch, the
  -- revision observed, anything that explains the classification. REL-03 wants
  -- the reasoning recoverable, not just the verdict.
  evidence jsonb NOT NULL DEFAULT '{}',
  created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  CHECK(ended_at IS NULL OR ended_at >= started_at)
);
-- One open obligation per channel: the reconciler writes through this
-- constraint instead of reading first, so two workers cannot open two.
CREATE UNIQUE INDEX recording_obligations_one_open ON recording_obligations(channel_id) WHERE ended_at IS NULL;
CREATE INDEX recording_obligations_channel_time ON recording_obligations(channel_id,started_at DESC,id DESC);
CREATE INDEX recording_obligations_window ON recording_obligations(started_at,ended_at);
-- Retention sweeps by closed time; an open obligation is current fact.
CREATE INDEX recording_obligations_retention ON recording_obligations(ended_at) WHERE ended_at IS NOT NULL;

-- Backfill: every channel that already has a policy gets one version row.
--
-- The effective time is deliberately not invented. This migration cannot know
-- when an existing policy was set, so it uses the channel's creation as a lower
-- bound (the policy cannot predate the channel) and labels the row 'backfill'.
-- A reader that needs exact boundaries can see the label and downgrade its
-- confidence; pretending the row was observed at the right instant would be
-- fabricating evidence, which is precisely what this chapter exists to prevent.
INSERT INTO recording_policy_versions(id,channel_id,version,mode,event_recording,effective_at,reason)
SELECT gen_random_uuid(),p.channel_id,p.version,p.mode,p.event_recording,c.created_at,'backfill'
FROM recording_policies p JOIN channels c ON c.id=p.channel_id;

-- And one open obligation per currently-recording channel, so the completeness
-- denominator exists from this migration onward instead of starting empty.
-- Channels not in 'continuous' get no obligation, which is the same verdict the
-- reconciler would reach on its first pass; writing it here just avoids a window
-- where an already-recording channel appears to owe nothing.
--
-- No placeholders: migrations are executed as one plain script, so the backfilled
-- start is the migration's own instant rather than a parameter.
INSERT INTO recording_obligations(id,channel_id,source_revision_id,policy_version,source,started_at,last_observed_at,evidence)
SELECT gen_random_uuid(),c.id,c.current_revision_id,p.version,'continuous',clock_timestamp(),clock_timestamp(),jsonb_build_object('derived','migration_backfill')
FROM channels c JOIN recording_policies p ON p.channel_id=c.id
WHERE c.enabled AND c.current_revision_id IS NOT NULL AND p.mode='continuous';
