-- Cloud archive batch 1: WebDAV targets, archive copies and the standalone
-- upload permission. PRD ARC-01/02/03/08(manual), FILE-02.

ALTER TABLE channel_grants ADD COLUMN archive boolean NOT NULL DEFAULT false;
-- PRD ARC-08: administrators hold the upload permission by default. At this
-- point only seeded admin accounts have grant rows at all.
UPDATE channel_grants g SET archive=true FROM users u WHERE u.id=g.user_id AND u.role='admin';

CREATE TABLE archive_targets (
  id uuid PRIMARY KEY,
  site_id uuid NOT NULL REFERENCES sites(id),
  name text NOT NULL CHECK(length(name) BETWEEN 1 AND 64),
  base_url text NOT NULL CHECK(base_url ~ '^https?://' AND length(base_url) BETWEEN 8 AND 512),
  remote_root text NOT NULL CHECK(left(remote_root,1)='/' AND left(remote_root,2)<>'//' AND remote_root<>'/'),
  username text NOT NULL CHECK(length(username) BETWEEN 1 AND 128),
  credential_key_id text NOT NULL CHECK(credential_key_id<>''),
  password_nonce bytea NOT NULL CHECK(octet_length(password_nonce)=12),
  password_ciphertext bytea NOT NULL CHECK(octet_length(password_ciphertext)>=16),
  cloud_retention_days integer NOT NULL DEFAULT 365 CHECK(cloud_retention_days>0),
  enabled boolean NOT NULL DEFAULT true,
  last_check jsonb,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE recording_archive_copies (
  id uuid PRIMARY KEY,
  segment_id uuid NOT NULL REFERENCES recording_segments(id),
  target_id uuid NOT NULL REFERENCES archive_targets(id),
  object_key text NOT NULL CHECK(left(object_key,1)<>'/' AND object_key<>'' AND right(object_key,1)<>'/'),
  state text NOT NULL DEFAULT 'uploading' CHECK(state IN ('uploading','verified','failed')),
  size_bytes bigint NOT NULL CHECK(size_bytes>0),
  sha256 text NOT NULL CHECK(sha256 ~ '^[0-9a-f]{64}$'),
  error_code text,
  uploaded_at timestamptz,
  verified_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE(segment_id,target_id)
);
CREATE INDEX recording_archive_copies_active ON recording_archive_copies(state) WHERE state='uploading';
