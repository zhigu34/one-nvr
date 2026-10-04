-- A late callback must always resolve the same physical source session.
CREATE FUNCTION protect_stream_session_identity() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF ROW(NEW.id,NEW.channel_id,NEW.source_revision_id,NEW.generation,NEW.vhost,NEW.app,NEW.stream,NEW.purpose)
 IS DISTINCT FROM ROW(OLD.id,OLD.channel_id,OLD.source_revision_id,OLD.generation,OLD.vhost,OLD.app,OLD.stream,OLD.purpose)
 THEN RAISE EXCEPTION 'stream session identity is immutable'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER stream_session_immutable BEFORE UPDATE ON stream_sessions
 FOR EACH ROW EXECUTE FUNCTION protect_stream_session_identity();
