-- M3 batch 2: per-channel detection configuration, versioned publication to
-- Frigate and the runtime capability snapshot. PRD AI-01/AI-07, §10.2.

ALTER TABLE channels ADD COLUMN detection_generation bigint NOT NULL DEFAULT 0 CHECK(detection_generation>=0);

CREATE TABLE detection_configs (
  channel_id uuid PRIMARY KEY REFERENCES channels(id),
  enabled boolean NOT NULL DEFAULT false,
  stream text NOT NULL DEFAULT 'sub' CHECK(stream IN ('main','sub')),
  fps integer NOT NULL DEFAULT 5 CHECK(fps BETWEEN 1 AND 30),
  labels jsonb NOT NULL DEFAULT '["person"]',
  version bigint NOT NULL DEFAULT 1 CHECK(version>0),
  updated_at timestamptz NOT NULL DEFAULT now()
);

-- One row per publication attempt: the rendered config hash and the outcome.
-- The file on disk is the truth Frigate reads; this table is the audit of how
-- it got there, so a failed publication never looks like a successful one.
CREATE TABLE detection_config_revisions (
  id uuid PRIMARY KEY,
  config_hash text NOT NULL CHECK(length(config_hash)=64),
  state text NOT NULL CHECK(state IN ('published','failed')),
  camera_count integer NOT NULL DEFAULT 0,
  error_code text,
  published_by uuid REFERENCES users(id),
  created_at timestamptz NOT NULL DEFAULT now()
);

-- Worker heartbeat of the upstream version/stats probe (AI-07).
CREATE TABLE detection_runtime (
  id text PRIMARY KEY CHECK(id='current'),
  version text NOT NULL DEFAULT '',
  detector text NOT NULL DEFAULT '',
  inference_ms real,
  overloaded boolean NOT NULL DEFAULT false,
  reachable boolean NOT NULL DEFAULT false,
  observed_at timestamptz NOT NULL DEFAULT now()
);
