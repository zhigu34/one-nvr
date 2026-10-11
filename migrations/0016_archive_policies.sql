-- Cloud archive batch 2: per-channel archive policy and the schedule dedupe
-- ledger. PRD ARC-05 (requirements fixed at publication) / ARC-08 (modes).

CREATE TABLE archive_policies (
  channel_id uuid PRIMARY KEY REFERENCES channels(id),
  target_id uuid NOT NULL REFERENCES archive_targets(id),
  mode text NOT NULL CHECK(mode IN ('manual','scheduled','immediate')),
  schedule text NOT NULL DEFAULT '' CHECK(schedule='' OR schedule ~ '^[0-2][0-9]:[0-5][0-9](,[0-2][0-9]:[0-5][0-9])*$'),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX archive_policies_target ON archive_policies(target_id);

-- One ledger row per (policy, local date, fire time): the UNIQUE constraint is
-- what keeps repeated workers and repeat triggers from enqueueing twice.
CREATE TABLE archive_schedule_runs (
  id uuid PRIMARY KEY,
  policy_id uuid NOT NULL REFERENCES archive_policies(channel_id) ON DELETE CASCADE,
  fire_date date NOT NULL,
  fire_time text NOT NULL CHECK(fire_time ~ '^[0-2][0-9]:[0-5][0-9]$'),
  queued integer NOT NULL DEFAULT 0,
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE(policy_id,fire_date,fire_time)
);
