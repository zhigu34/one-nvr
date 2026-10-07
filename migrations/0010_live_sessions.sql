CREATE TABLE live_sessions (
 id uuid PRIMARY KEY,
 user_id uuid NOT NULL REFERENCES users(id),
 session_id uuid NOT NULL REFERENCES sessions(id),
 auth_version bigint NOT NULL,
 channel_id uuid NOT NULL REFERENCES channels(id),
 stream_session_id uuid NOT NULL REFERENCES stream_sessions(id),
 ticket_hash bytea NOT NULL UNIQUE CHECK(octet_length(ticket_hash)=32),
 state text NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','active','closing','closed')),
 peer_id text, peer_token text,
 cleanup_after timestamptz NOT NULL DEFAULT clock_timestamp(),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 expires_at timestamptz NOT NULL DEFAULT clock_timestamp()+interval '30 seconds',
 CHECK((peer_id IS NULL)=(peer_token IS NULL))
);
CREATE INDEX live_session_recovery ON live_sessions(state,expires_at);
