-- Two global persisted source-test slots, fenced by the owning job lease.
-- Waiting jobs are not claimed, so capacity pressure consumes no attempts.
CREATE TABLE source_test_slots (
 slot smallint PRIMARY KEY CHECK(slot IN (1,2)),
 job_id uuid UNIQUE REFERENCES jobs(id), fencing_token uuid,
 CHECK((job_id IS NULL)=(fencing_token IS NULL))
);
INSERT INTO source_test_slots(slot) VALUES(1),(2);
-- Associate pre-allocated physical side effects with their immutable intent.
ALTER TABLE source_switches ADD CONSTRAINT switch_channel_identity UNIQUE(channel_id,id);
ALTER TABLE stream_sessions
 ADD COLUMN source_test_id uuid,
 ADD COLUMN switch_id uuid,
 ADD CONSTRAINT stream_test_channel FOREIGN KEY(channel_id,source_test_id) REFERENCES source_tests(channel_id,id),
 ADD CONSTRAINT stream_switch_channel FOREIGN KEY(channel_id,switch_id) REFERENCES source_switches(channel_id,id),
 ADD COLUMN operation_role text CHECK(operation_role IN ('test_main','test_sub','candidate_main','candidate_sub','rollback_main','rollback_sub')),
 ADD CONSTRAINT stream_session_single_intent CHECK(source_test_id IS NULL OR switch_id IS NULL),
 ADD CONSTRAINT stream_session_test_role CHECK(source_test_id IS NULL OR (purpose='test' AND operation_role IN ('test_main','test_sub'))),
 ADD CONSTRAINT stream_session_switch_role CHECK(switch_id IS NULL OR ((purpose='main' AND operation_role IN ('candidate_main','rollback_main')) OR (purpose='sub' AND operation_role IN ('candidate_sub','rollback_sub'))));
CREATE INDEX stream_test_intent ON stream_sessions(source_test_id,created_at DESC) WHERE source_test_id IS NOT NULL;
CREATE INDEX stream_switch_intent ON stream_sessions(switch_id,created_at DESC) WHERE switch_id IS NOT NULL;
CREATE FUNCTION protect_stream_session_intent() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF ROW(NEW.source_test_id,NEW.switch_id,NEW.operation_role) IS DISTINCT FROM ROW(OLD.source_test_id,OLD.switch_id,OLD.operation_role)
 THEN RAISE EXCEPTION 'stream session intent is immutable'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER stream_session_intent_immutable BEFORE UPDATE ON stream_sessions FOR EACH ROW EXECUTE FUNCTION protect_stream_session_intent();
