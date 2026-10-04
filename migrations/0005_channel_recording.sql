-- Additive channel/media schema. Existing policies and identities stay intact.
ALTER TABLE channels ADD COLUMN enabled boolean NOT NULL DEFAULT true,
 ADD COLUMN current_revision_id uuid, ADD COLUMN desired_revision_id uuid,
 ADD COLUMN storage_pool_id uuid REFERENCES storage_pools(id),
 ADD COLUMN source_generation bigint NOT NULL DEFAULT 0 CHECK(source_generation>=0),
 ADD CONSTRAINT channels_site_identity UNIQUE(site_id,id);
ALTER TABLE storage_pools ADD CONSTRAINT storage_pool_site_identity UNIQUE(site_id,id);

CREATE TABLE source_identities (
 id uuid PRIMARY KEY, channel_id uuid NOT NULL REFERENCES channels(id),
 label text NOT NULL DEFAULT '', confidence text NOT NULL DEFAULT 'user_declared'
 CHECK(confidence IN ('user_declared','verified','unknown')),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(), UNIQUE(channel_id,id)
);
CREATE TABLE source_revisions (
 id uuid PRIMARY KEY, channel_id uuid NOT NULL REFERENCES channels(id), source_id uuid NOT NULL,
 revision_no bigint NOT NULL CHECK(revision_no>0),
 ip inet NOT NULL CHECK(masklen(ip)=CASE family(ip) WHEN 4 THEN 32 ELSE 128 END),
 rtsp_port integer NOT NULL DEFAULT 554 CHECK(rtsp_port BETWEEN 1 AND 65535),
 main_path text NOT NULL CHECK(left(main_path,1)='/' AND left(main_path,2)<>'//'),
 sub_path text NOT NULL DEFAULT '' CHECK(sub_path='' OR (left(sub_path,1)='/' AND left(sub_path,2)<>'//')),
 transport text NOT NULL DEFAULT 'tcp' CHECK(transport IN ('tcp','udp')),
 onvif_port integer CHECK(onvif_port BETWEEN 1 AND 65535),
 credential_key_id text NOT NULL CHECK(credential_key_id<>''),
 username_nonce bytea NOT NULL CHECK(octet_length(username_nonce)=12),
 username_ciphertext bytea NOT NULL CHECK(octet_length(username_ciphertext)>=16),
 password_nonce bytea NOT NULL CHECK(octet_length(password_nonce)=12),
 password_ciphertext bytea NOT NULL CHECK(octet_length(password_ciphertext)>=16),
 username_summary text NOT NULL DEFAULT '', password_state text NOT NULL DEFAULT 'saved' CHECK(password_state IN ('saved','empty')),
 configuration_digest bytea CHECK(configuration_digest IS NULL OR octet_length(configuration_digest)=32),
 created_by uuid REFERENCES users(id), created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(channel_id,id), UNIQUE(channel_id,revision_no),
 FOREIGN KEY(channel_id,source_id) REFERENCES source_identities(channel_id,id)
);
CREATE FUNCTION reject_source_revision_update() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'source revisions are immutable'; END $$;
CREATE TRIGGER source_revision_immutable BEFORE UPDATE ON source_revisions
 FOR EACH ROW EXECUTE FUNCTION reject_source_revision_update();
ALTER TABLE channels ADD CONSTRAINT current_source_belongs_to_channel
 FOREIGN KEY(id,current_revision_id) REFERENCES source_revisions(channel_id,id),
 ADD CONSTRAINT desired_source_belongs_to_channel
 FOREIGN KEY(id,desired_revision_id) REFERENCES source_revisions(channel_id,id);

CREATE TABLE source_tests (
 id uuid PRIMARY KEY, channel_id uuid NOT NULL REFERENCES channels(id), revision_id uuid NOT NULL,
 job_id uuid NOT NULL UNIQUE REFERENCES jobs(id), purpose text NOT NULL DEFAULT 'source' CHECK(purpose IN ('source','pool')),
 state text NOT NULL DEFAULT 'queued' CHECK(state IN ('queued','testing','succeeded','failed','cancelled')),
 configuration_digest bytea NOT NULL CHECK(octet_length(configuration_digest)=32),
 result jsonb NOT NULL DEFAULT '{}', error_code text,
 observed_at timestamptz, expires_at timestamptz,
 created_by uuid NOT NULL REFERENCES users(id), created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(channel_id,id), FOREIGN KEY(channel_id,revision_id) REFERENCES source_revisions(channel_id,id),
 CHECK(expires_at IS NULL OR (observed_at IS NOT NULL AND expires_at>observed_at))
);
CREATE INDEX source_test_history ON source_tests(channel_id,created_at DESC,id DESC);
CREATE TABLE source_switches (
 id uuid PRIMARY KEY, channel_id uuid NOT NULL REFERENCES channels(id), job_id uuid NOT NULL UNIQUE REFERENCES jobs(id),
 kind text NOT NULL DEFAULT 'apply' CHECK(kind IN ('apply','clear','pool_switch','policy_apply','enable')),
 old_revision_id uuid, new_revision_id uuid, test_id uuid,
 old_pool_id uuid REFERENCES storage_pools(id), new_pool_id uuid REFERENCES storage_pools(id),
 old_mode text CHECK(old_mode IN ('none','continuous','planned')),
 desired_mode text CHECK(desired_mode IN ('none','continuous')), expected_version bigint NOT NULL CHECK(expected_version>0),
 state text NOT NULL DEFAULT 'queued' CHECK(state IN ('queued','running','succeeded','rolled_back','failed','cancelled')),
 phase text NOT NULL DEFAULT 'queued' CHECK(phase IN ('queued','testing','stopping_old','starting_new','verifying_new','committed','rolling_back','rolled_back','failed','cancelled')),
 phase_deadline timestamptz, started_at timestamptz, finished_at timestamptz,
 fencing_token uuid, error_code text, created_by uuid REFERENCES users(id),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 FOREIGN KEY(channel_id,old_revision_id) REFERENCES source_revisions(channel_id,id),
 FOREIGN KEY(channel_id,new_revision_id) REFERENCES source_revisions(channel_id,id),
 FOREIGN KEY(channel_id,test_id) REFERENCES source_tests(channel_id,id)
);
CREATE UNIQUE INDEX one_channel_source_change ON source_switches(channel_id) WHERE state IN ('queued','running');

CREATE TABLE source_import_batches (
 id uuid PRIMARY KEY, site_id uuid NOT NULL REFERENCES sites(id), created_by uuid NOT NULL REFERENCES users(id),
 format text NOT NULL CHECK(format IN ('csv','json')), format_version integer NOT NULL DEFAULT 1 CHECK(format_version=1),
 state text NOT NULL DEFAULT 'preview' CHECK(state IN ('preview','submitted','running','succeeded','partial','failed','cancelled')),
 job_id uuid REFERENCES jobs(id), created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 expires_at timestamptz NOT NULL, submitted_at timestamptz, cancel_requested boolean NOT NULL DEFAULT false,
 CHECK(expires_at>created_at)
);
CREATE TABLE source_import_items (
 id uuid PRIMARY KEY, batch_id uuid NOT NULL REFERENCES source_import_batches(id), row_no integer NOT NULL CHECK(row_no BETWEEN 1 AND 32),
 channel_id uuid REFERENCES channels(id), expected_version bigint CHECK(expected_version>0),
 draft_nonce bytea CHECK(draft_nonce IS NULL OR octet_length(draft_nonce)=12), draft_ciphertext bytea,
 credential_key_id text, summary jsonb NOT NULL DEFAULT '{}', selection jsonb,
 state text NOT NULL DEFAULT 'draft' CHECK(state IN ('draft','testing','tested','queued','running','succeeded','skipped','failed','cancelled')),
 revision_id uuid REFERENCES source_revisions(id), test_id uuid REFERENCES source_tests(id), switch_id uuid REFERENCES source_switches(id),
 error_code text, updated_at timestamptz NOT NULL DEFAULT clock_timestamp(), UNIQUE(batch_id,row_no),
 CHECK((draft_nonce IS NULL AND draft_ciphertext IS NULL AND credential_key_id IS NULL) OR
 (draft_nonce IS NOT NULL AND draft_ciphertext IS NOT NULL AND octet_length(draft_ciphertext)>=16 AND credential_key_id IS NOT NULL))
);
CREATE UNIQUE INDEX import_target_unique ON source_import_items(batch_id,channel_id) WHERE channel_id IS NOT NULL;

CREATE TABLE stream_sessions (
 id uuid PRIMARY KEY, channel_id uuid NOT NULL REFERENCES channels(id), source_revision_id uuid NOT NULL,
 generation bigint NOT NULL CHECK(generation>0),
 vhost text NOT NULL DEFAULT '__defaultVhost__', app text NOT NULL, stream text NOT NULL,
 purpose text NOT NULL CHECK(purpose IN ('main','sub','test')),
 state text NOT NULL DEFAULT 'starting' CHECK(state IN ('starting','active','closing','closed','failed')),
 proxy_key text, started_at timestamptz, closed_at timestamptz, error_code text,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(vhost,app,stream), UNIQUE(channel_id,id,source_revision_id),
 FOREIGN KEY(channel_id,source_revision_id) REFERENCES source_revisions(channel_id,id)
);
CREATE INDEX stream_session_recovery ON stream_sessions(channel_id,state);
CREATE TABLE recording_runs (
 id uuid PRIMARY KEY, site_id uuid NOT NULL REFERENCES sites(id),
 channel_id uuid NOT NULL, source_revision_id uuid NOT NULL, stream_session_id uuid NOT NULL,
 pool_id uuid NOT NULL, work_relative_path text NOT NULL CHECK(work_relative_path<>'' AND left(work_relative_path,1)<> '/'),
 purpose text NOT NULL CHECK(purpose IN ('probe','continuous')),
 state text NOT NULL DEFAULT 'starting' CHECK(state IN ('starting','recording','stopping','stopped','failed')),
 started_at timestamptz, ended_at timestamptz, last_completion_at timestamptz, error_code text,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(pool_id,work_relative_path), UNIQUE(channel_id,source_revision_id,id,pool_id),
 FOREIGN KEY(site_id,channel_id) REFERENCES channels(site_id,id),
 FOREIGN KEY(site_id,pool_id) REFERENCES storage_pools(site_id,id),
 FOREIGN KEY(channel_id,stream_session_id,source_revision_id) REFERENCES stream_sessions(channel_id,id,source_revision_id)
);
CREATE FUNCTION protect_recording_run_identity() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF ROW(NEW.id,NEW.site_id,NEW.channel_id,NEW.source_revision_id,NEW.stream_session_id,NEW.pool_id,NEW.work_relative_path,NEW.purpose)
 IS DISTINCT FROM ROW(OLD.id,OLD.site_id,OLD.channel_id,OLD.source_revision_id,OLD.stream_session_id,OLD.pool_id,OLD.work_relative_path,OLD.purpose)
 THEN RAISE EXCEPTION 'recording run identity is immutable'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER recording_run_immutable BEFORE UPDATE ON recording_runs
 FOR EACH ROW EXECUTE FUNCTION protect_recording_run_identity();
CREATE INDEX recording_run_recovery ON recording_runs(state,created_at);
CREATE UNIQUE INDEX one_active_recorder_per_session ON recording_runs(stream_session_id)
 WHERE state IN ('starting','recording','stopping');

CREATE TABLE hook_inbox (
 id uuid PRIMARY KEY, source text NOT NULL DEFAULT 'zlm', source_key text NOT NULL,
 run_id uuid REFERENCES recording_runs(id), payload jsonb NOT NULL,
 state text NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','processing','processed','diagnostic')),
 fencing_token uuid, lease_expires_at timestamptz, available_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 attempts integer NOT NULL DEFAULT 0 CHECK(attempts>=0), error_code text,
 received_at timestamptz NOT NULL DEFAULT clock_timestamp(), processed_at timestamptz,
 UNIQUE(source,source_key)
);
CREATE INDEX hook_inbox_recovery ON hook_inbox(available_at) WHERE state IN ('pending','processing');
CREATE TABLE recording_segments (
 id uuid PRIMARY KEY, channel_id uuid NOT NULL, source_revision_id uuid NOT NULL,
 run_id uuid NOT NULL, pool_id uuid NOT NULL,
 original_relative_path text NOT NULL, target_relative_path text NOT NULL,
 naming_rule_version integer NOT NULL DEFAULT 3 CHECK(naming_rule_version=3),
 original_start timestamptz NOT NULL, naming_timezone text NOT NULL,
 utc_offset_seconds integer NOT NULL CHECK(utc_offset_seconds BETWEEN -86400 AND 86400),
 start_at timestamptz NOT NULL, end_at timestamptz NOT NULL, size_bytes bigint NOT NULL CHECK(size_bytes>0),
 file_identity jsonb NOT NULL DEFAULT '{}', media_evidence jsonb NOT NULL DEFAULT '{}',
 state text NOT NULL DEFAULT 'finalizing' CHECK(state IN ('finalizing','ready','missing','damaged','conflict','provisional')),
 time_evidence text NOT NULL DEFAULT 'completion' CHECK(time_evidence IN ('completion','recovered','provisional')),
 error_code text, created_at timestamptz NOT NULL DEFAULT clock_timestamp(), ready_at timestamptz,
 UNIQUE(run_id,original_relative_path), UNIQUE(pool_id,target_relative_path), UNIQUE(id,pool_id),
 FOREIGN KEY(channel_id,source_revision_id,run_id,pool_id) REFERENCES recording_runs(channel_id,source_revision_id,id,pool_id),
 CHECK(end_at>start_at),
 CHECK(original_relative_path<>'' AND left(original_relative_path,1)<>'/'),
 CHECK(target_relative_path<>'' AND left(target_relative_path,1)<>'/')
);
CREATE INDEX recording_channel_time ON recording_segments(channel_id,start_at DESC,id DESC);
CREATE INDEX recording_publish_recovery ON recording_segments(created_at) WHERE state='finalizing';
CREATE FUNCTION protect_recording_publication_identity() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF ROW(NEW.id,NEW.channel_id,NEW.source_revision_id,NEW.run_id,NEW.pool_id,NEW.original_relative_path,NEW.target_relative_path,NEW.naming_rule_version,NEW.original_start,NEW.naming_timezone,NEW.utc_offset_seconds)
 IS DISTINCT FROM ROW(OLD.id,OLD.channel_id,OLD.source_revision_id,OLD.run_id,OLD.pool_id,OLD.original_relative_path,OLD.target_relative_path,OLD.naming_rule_version,OLD.original_start,OLD.naming_timezone,OLD.utc_offset_seconds)
 THEN RAISE EXCEPTION 'recording publication identity is immutable'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER recording_publication_immutable BEFORE UPDATE ON recording_segments
 FOR EACH ROW EXECUTE FUNCTION protect_recording_publication_identity();
CREATE TABLE recording_locations (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), segment_id uuid NOT NULL, pool_id uuid NOT NULL,
 relative_path text NOT NULL CHECK(relative_path<>'' AND left(relative_path,1)<>'/'),
 size_bytes bigint NOT NULL CHECK(size_bytes>0),
 state text NOT NULL DEFAULT 'ready' CHECK(state IN ('ready','missing','damaged')),
 verified_at timestamptz NOT NULL DEFAULT clock_timestamp(), UNIQUE(pool_id,relative_path), UNIQUE(segment_id,pool_id),
 FOREIGN KEY(segment_id,pool_id) REFERENCES recording_segments(id,pool_id)
);
CREATE TABLE recording_gaps (
 id uuid PRIMARY KEY, channel_id uuid NOT NULL REFERENCES channels(id),
 source_revision_id uuid, switch_id uuid REFERENCES source_switches(id), run_id uuid REFERENCES recording_runs(id),
 start_at timestamptz NOT NULL, end_at timestamptz,
 reason_code text NOT NULL, evidence text NOT NULL DEFAULT 'observed' CHECK(evidence IN ('observed','unknown')),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 FOREIGN KEY(channel_id,source_revision_id) REFERENCES source_revisions(channel_id,id), CHECK(end_at IS NULL OR end_at>=start_at)
);
CREATE INDEX recording_gap_time ON recording_gaps(channel_id,start_at DESC,id DESC);
CREATE TABLE source_observations (
 id uuid PRIMARY KEY, channel_id uuid NOT NULL REFERENCES channels(id), source_revision_id uuid,
 session_id uuid, kind text NOT NULL CHECK(kind IN ('main','sub','recording')),
 state text NOT NULL CHECK(state IN ('healthy','unavailable','unknown','disabled','not_configured','degraded')),
 reason_code text NOT NULL, evidence jsonb NOT NULL DEFAULT '{}',
 observed_at timestamptz NOT NULL, expires_at timestamptz NOT NULL,
 FOREIGN KEY(channel_id,source_revision_id) REFERENCES source_revisions(channel_id,id), CHECK(expires_at>observed_at)
);
CREATE INDEX source_observation_latest ON source_observations(channel_id,kind,observed_at DESC,id DESC);
