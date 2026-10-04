-- Synchronous draft retries need their own outcome; they are not worker jobs.
CREATE TABLE source_draft_requests (
 actor_id uuid NOT NULL REFERENCES users(id), request_key text NOT NULL CHECK(char_length(request_key) BETWEEN 1 AND 128),
 channel_id uuid NOT NULL REFERENCES channels(id), parameter_digest bytea NOT NULL CHECK(octet_length(parameter_digest)=32),
 revision_id uuid NOT NULL, created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(actor_id,request_key), FOREIGN KEY(channel_id,revision_id) REFERENCES source_revisions(channel_id,id)
);
