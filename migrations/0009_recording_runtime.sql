-- Runtime owners share the same pinned channel lock as explicit source jobs.
-- Sampling does not create an unbounded stream of user jobs.
CREATE TABLE channel_runtime_leases (
 channel_id uuid PRIMARY KEY REFERENCES channels(id),
 fencing_token uuid NOT NULL, configuration_version bigint NOT NULL CHECK(configuration_version>0),
 expires_at timestamptz NOT NULL
);
CREATE TABLE recording_bitrate_samples (
 id uuid PRIMARY KEY, channel_id uuid NOT NULL, source_revision_id uuid NOT NULL,
 stream_session_id uuid NOT NULL, source_test_id uuid,
 bytes_per_second bigint NOT NULL CHECK(bytes_per_second>=0), frames bigint NOT NULL CHECK(frames>=0),
 valid boolean NOT NULL, observed_at timestamptz NOT NULL,
 FOREIGN KEY(channel_id,stream_session_id,source_revision_id) REFERENCES stream_sessions(channel_id,id,source_revision_id),
 FOREIGN KEY(channel_id,source_test_id) REFERENCES source_tests(channel_id,id)
);
CREATE INDEX bitrate_recent ON recording_bitrate_samples(channel_id,source_revision_id,observed_at DESC);
CREATE TABLE recording_capacity_blocks (
 channel_id uuid PRIMARY KEY REFERENCES channels(id),
 reason_code text NOT NULL, healthy_samples integer NOT NULL DEFAULT 0 CHECK(healthy_samples BETWEEN 0 AND 2),
 last_healthy_at timestamptz, blocked_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
-- A probe can crash after AddProxy and before its recording run exists.
CREATE TABLE pool_probe_intents (
 session_id uuid PRIMARY KEY REFERENCES stream_sessions(id),
 pool_id uuid NOT NULL REFERENCES storage_pools(id),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
