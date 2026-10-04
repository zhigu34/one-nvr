-- Persist the active public scheme so redeployment can revoke old sessions.
CREATE TABLE auth_entry_protocol (
 singleton boolean PRIMARY KEY DEFAULT true CHECK(singleton),
 scheme text NOT NULL CHECK(scheme IN ('http','https')),
 changed_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
