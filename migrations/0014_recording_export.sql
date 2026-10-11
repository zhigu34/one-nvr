-- Recording range export: async concat tasks, their artifacts and the pinned
-- source segments they read from. PRD EXP-01..05, FILE-06.

CREATE TABLE recording_exports (
  id uuid PRIMARY KEY,
  channel_id uuid NOT NULL REFERENCES channels(id),
  channel_no integer NOT NULL CHECK(channel_no>0),
  pool_id uuid NOT NULL REFERENCES storage_pools(id),
  requested_start timestamptz NOT NULL,
  requested_end timestamptz NOT NULL CHECK(requested_end>requested_start),
  gap_mode text NOT NULL CHECK(gap_mode IN ('merge','split')),
  state text NOT NULL DEFAULT 'queued' CHECK(state IN ('queued','running','succeeded','failed')),
  progress smallint NOT NULL DEFAULT 0 CHECK(progress BETWEEN 0 AND 100),
  message text NOT NULL DEFAULT '',
  error_code text,
  actual_start timestamptz,
  actual_end timestamptz,
  requested_by uuid NOT NULL REFERENCES users(id),
  requested_by_name text NOT NULL CHECK(requested_by_name<>''),
  idempotency_key text NOT NULL CHECK(idempotency_key<>''),
  job_id uuid NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE(idempotency_key)
);
CREATE INDEX recording_exports_channel ON recording_exports(channel_id,created_at DESC);
CREATE INDEX recording_exports_active ON recording_exports(state) WHERE state IN ('queued','running');

-- One export produces one artifact per contiguous span (merge mode: exactly one).
-- The manifest itself is generated at download time, never stored: the hash and
-- the file live here, the evidence document is derived.
CREATE TABLE recording_export_artifacts (
  id uuid PRIMARY KEY,
  export_id uuid NOT NULL REFERENCES recording_exports(id) ON DELETE CASCADE,
  pool_id uuid NOT NULL REFERENCES storage_pools(id),
  relative_path text NOT NULL CHECK(relative_path<>'' AND left(relative_path,1)<>'/'),
  file_name text NOT NULL CHECK(file_name<>'' AND position('/' IN file_name)=0),
  size_bytes bigint NOT NULL CHECK(size_bytes>0),
  sha256 text NOT NULL CHECK(sha256 ~ '^[0-9a-f]{64}$'),
  start_at timestamptz NOT NULL,
  end_at timestamptz NOT NULL CHECK(end_at>start_at),
  segment_count integer NOT NULL CHECK(segment_count>0),
  state text NOT NULL DEFAULT 'ready' CHECK(state IN ('ready','expired')),
  protected boolean NOT NULL DEFAULT false,
  expires_at timestamptz NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE(pool_id,relative_path)
);
CREATE INDEX recording_export_artifacts_expiry ON recording_export_artifacts(expires_at) WHERE state='ready';

-- The pinned source segments in concat order. While the export is queued or
-- running these rows are a temporary evidence reference: retention and manual
-- deletion skip a segment they cover, so a task cannot have its input removed
-- underneath it. Terminal exports no longer hold anything.
CREATE TABLE recording_export_sources (
  export_id uuid NOT NULL REFERENCES recording_exports(id) ON DELETE CASCADE,
  seq integer NOT NULL CHECK(seq>0),
  segment_id uuid NOT NULL REFERENCES recording_segments(id),
  PRIMARY KEY(export_id,seq)
);
CREATE INDEX recording_export_sources_segment ON recording_export_sources(segment_id);
