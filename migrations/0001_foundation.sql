CREATE TABLE sites (
 id uuid PRIMARY KEY, singleton boolean NOT NULL DEFAULT true UNIQUE CHECK(singleton),
 name text NOT NULL, timezone text NOT NULL DEFAULT 'Asia/Shanghai',
 channel_count integer NOT NULL CHECK(channel_count IN (16,32)),
 version bigint NOT NULL DEFAULT 1 CHECK(version>0), created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE TABLE users (
 id uuid PRIMARY KEY, site_id uuid NOT NULL REFERENCES sites(id), username text NOT NULL,
 password_hash text NOT NULL, role text NOT NULL CHECK(role IN ('admin','operator','viewer')),
 enabled boolean NOT NULL DEFAULT true, auth_version bigint NOT NULL DEFAULT 1,
 version bigint NOT NULL DEFAULT 1, created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 CHECK(char_length(username) BETWEEN 1 AND 64)
);
CREATE UNIQUE INDEX users_username_unique ON users(lower(username));
CREATE TABLE channels (
 id uuid PRIMARY KEY, site_id uuid NOT NULL REFERENCES sites(id), channel_no integer NOT NULL CHECK(channel_no BETWEEN 1 AND 32),
 channel_name text NOT NULL, version bigint NOT NULL DEFAULT 1,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(), UNIQUE(site_id,channel_no)
);
CREATE TABLE channel_grants (
 user_id uuid NOT NULL REFERENCES users(id), channel_id uuid NOT NULL REFERENCES channels(id),
 live boolean NOT NULL DEFAULT false, playback boolean NOT NULL DEFAULT false,
 export boolean NOT NULL DEFAULT false, configure boolean NOT NULL DEFAULT false,
 PRIMARY KEY(user_id,channel_id)
);
CREATE TABLE sessions (
 id uuid PRIMARY KEY, digest bytea NOT NULL UNIQUE CHECK(octet_length(digest)=32),
 user_id uuid NOT NULL REFERENCES users(id), auth_version bigint NOT NULL,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(), last_seen_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 absolute_expires_at timestamptz NOT NULL, revoked_at timestamptz
);
CREATE INDEX sessions_user ON sessions(user_id);
CREATE TABLE recording_policies (
 channel_id uuid PRIMARY KEY REFERENCES channels(id), mode text NOT NULL DEFAULT 'none' CHECK(mode IN ('none','continuous','planned')),
 event_recording boolean NOT NULL DEFAULT false, version bigint NOT NULL DEFAULT 1
);
CREATE TABLE storage_pools (
 id uuid PRIMARY KEY, site_id uuid NOT NULL REFERENCES sites(id), name text NOT NULL,
 canonical_path text NOT NULL UNIQUE, filesystem_id text, enabled boolean NOT NULL DEFAULT true,
 is_default boolean NOT NULL DEFAULT false, version bigint NOT NULL DEFAULT 1,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE UNIQUE INDEX one_default_pool ON storage_pools(site_id) WHERE is_default;
CREATE TABLE storage_pool_checks (
 pool_id uuid NOT NULL REFERENCES storage_pools(id), service text NOT NULL CHECK(service IN ('api','worker','zlm')),
 state text NOT NULL CHECK(state IN ('healthy','unavailable','pending')), reason_code text NOT NULL,
 observed_at timestamptz NOT NULL, expires_at timestamptz NOT NULL,
 free_bytes bigint, total_bytes bigint, filesystem_id text,
 PRIMARY KEY(pool_id,service)
);
CREATE TABLE jobs (
 id uuid PRIMARY KEY, kind text NOT NULL, object_id uuid,
 idempotency_key text NOT NULL, parameter_digest bytea NOT NULL CHECK(octet_length(parameter_digest)=32),
 payload jsonb NOT NULL DEFAULT '{}', state text NOT NULL DEFAULT 'queued' CHECK(state IN ('queued','running','succeeded','failed')),
 attempt integer NOT NULL DEFAULT 0, max_attempts integer NOT NULL DEFAULT 6 CHECK(max_attempts BETWEEN 1 AND 20),
 fencing_token uuid, lease_expires_at timestamptz, available_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 result jsonb, error_code text, created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(), UNIQUE(kind,idempotency_key)
);
CREATE INDEX jobs_claim ON jobs(kind,available_at) WHERE state IN ('queued','running');
CREATE TABLE job_attempts (
 job_id uuid NOT NULL REFERENCES jobs(id), attempt integer NOT NULL,
 fencing_token uuid NOT NULL, state text NOT NULL CHECK(state IN ('running','succeeded','retry','failed','lease_expired')),
 started_at timestamptz NOT NULL DEFAULT clock_timestamp(), finished_at timestamptz, error_code text,
 PRIMARY KEY(job_id,attempt)
);
CREATE TABLE component_observations (
 name text PRIMARY KEY, state text NOT NULL CHECK(state IN ('healthy','unavailable','unknown')),
 reason_code text NOT NULL, observed_at timestamptz NOT NULL, expires_at timestamptz NOT NULL
);
CREATE TABLE audit_logs (
 id uuid PRIMARY KEY, actor_id uuid REFERENCES users(id), action text NOT NULL, object_id uuid,
 details jsonb NOT NULL DEFAULT '{}', created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE INDEX audit_cursor ON audit_logs(created_at DESC,id DESC);
CREATE TABLE tls_certificates (
 id uuid PRIMARY KEY, source text NOT NULL CHECK(source IN ('manual','directory','self_signed')),
 metadata jsonb NOT NULL, content_digest bytea NOT NULL UNIQUE,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE TABLE gateway_tls_state (
 singleton boolean PRIMARY KEY DEFAULT true CHECK(singleton), active_id uuid REFERENCES tls_certificates(id),
 previous_id uuid REFERENCES tls_certificates(id), desired_id uuid REFERENCES tls_certificates(id),
 source text NOT NULL CHECK(source IN ('manual','directory')), auto_apply boolean NOT NULL DEFAULT true,
 state text NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','active','unavailable','applying')),
 version bigint NOT NULL DEFAULT 1, last_check_at timestamptz, last_apply_at timestamptz, error_code text
);
