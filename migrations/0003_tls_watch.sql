ALTER TABLE gateway_tls_state
 ADD COLUMN candidate_id uuid REFERENCES tls_certificates(id),
 ADD COLUMN check_state text NOT NULL DEFAULT 'pending' CHECK(check_state IN ('pending','stabilizing','valid','unavailable','manual')),
 ADD COLUMN check_reason text NOT NULL DEFAULT 'not_checked',
 ADD COLUMN consecutive_errors integer NOT NULL DEFAULT 0 CHECK(consecutive_errors>=0);
