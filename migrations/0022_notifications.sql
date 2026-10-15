-- M3 batch 4: notification (EVT-05) and snapshot archiving. PRD EVT-05, AI-05,
-- milestone row "通知、抓拍归档", STO-07.
--
-- The design point is the split between *what happened* and *how it is told*:
-- a rule hit produces one channel-independent notification (the message), and
-- each bound channel is only a way to carry that message out. Adding a channel
-- kind therefore adds rows here, never a change to the message.

CREATE TABLE notification_channels (
  id uuid PRIMARY KEY,
  site_id uuid NOT NULL REFERENCES sites(id),
  -- inapp is the built-in inbox channel and needs no endpoint; webhook posts
  -- the message as JSON. Later kinds (email, wecom, ...) are added as adapters
  -- and only require widening this list.
  kind text NOT NULL CHECK(kind IN ('inapp','webhook')),
  name text NOT NULL CHECK(length(name) BETWEEN 1 AND 64),
  -- Endpoint for kinds that push. Null for the inbox.
  url text,
  -- Delivery secret, sealed with the deployment key exactly like archive
  -- target passwords: key id, nonce and ciphertext travel separately and the
  -- plaintext never reaches a response, a log or an audit row. Used to sign
  -- the request body so a receiver can verify origin.
  credential_key_id text NOT NULL CHECK(credential_key_id<>''),
  secret_nonce bytea NOT NULL CHECK(octet_length(secret_nonce)=12),
  secret_ciphertext bytea NOT NULL CHECK(octet_length(secret_ciphertext)>0),
  enabled boolean NOT NULL DEFAULT true,
  -- Outcome of the last explicit test delivery, so the operator sees what the
  -- receiver actually answered instead of a claim that it "works".
  last_check jsonb,
  version bigint NOT NULL DEFAULT 1 CHECK(version>0),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  -- A pushing channel cannot be saved without a target; the inbox cannot have
  -- one.
  CHECK((kind='inapp' AND url IS NULL) OR (kind='webhook' AND url IS NOT NULL))
);

CREATE TABLE notifications (
  id uuid PRIMARY KEY,
  -- The hit this notification was raised for. Nullable so an event-level
  -- notice (a fault, a test) can reuse the same inbox without inventing a hit.
  hit_id uuid REFERENCES event_rule_hits(id) ON DELETE CASCADE,
  event_id uuid REFERENCES events(id) ON DELETE CASCADE,
  channel_id uuid NOT NULL REFERENCES channels(id),
  rule_id uuid REFERENCES detection_rules(id) ON DELETE SET NULL,
  -- Stable delivery identity: minted once when the notification is raised and
  -- carried unchanged through every retry so a receiver can deduplicate
  -- (EVT-05). It is the message's DeliveryID, stored here so the inbox and the
  -- outbound adapters describe the same delivery.
  delivery_id uuid NOT NULL UNIQUE,
  -- The frozen message. Facts are copied at raise time, so a later rename of a
  -- channel, a rule or a zone cannot rewrite what was already told (the same
  -- reasoning as event_rule_hits.reason).
  message jsonb NOT NULL CHECK(jsonb_typeof(message)='object'),
  severity text NOT NULL CHECK(severity IN ('low','medium','high')),
  -- Inbox read state. Server-side so every device agrees.
  read_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX notifications_recent ON notifications(created_at DESC,id DESC);
CREATE INDEX notifications_unread ON notifications(created_at DESC) WHERE read_at IS NULL;
CREATE INDEX notifications_event ON notifications(event_id);
-- Cooldown lookups ask "was this rule on this channel told recently?".
CREATE INDEX notifications_cooldown ON notifications(rule_id,channel_id,created_at DESC) WHERE rule_id IS NOT NULL;

CREATE TABLE notification_deliveries (
  id uuid PRIMARY KEY,
  notification_id uuid NOT NULL REFERENCES notifications(id) ON DELETE CASCADE,
  channel_id uuid NOT NULL REFERENCES notification_channels(id) ON DELETE CASCADE,
  -- sending: queued or being attempted; failed: this attempt failed and a
  -- retry is coming; retrying: failed at least once and still within the retry
  -- window; succeeded: the receiver answered 2xx; exhausted: out of attempts or
  -- permanently refused. Attempt counts live in job_attempts, never here, so
  -- the count has exactly one source of truth.
  state text NOT NULL CHECK(state IN ('sending','failed','retrying','succeeded','exhausted')),
  job_id uuid,
  -- What the far end actually said, kept for diagnosis: HTTP status, a
  -- transport reason. Never carries the secret.
  last_error text NOT NULL DEFAULT '',
  last_http_status integer CHECK(last_http_status IS NULL OR (last_http_status BETWEEN 100 AND 599)),
  delivered_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  -- One delivery per channel per notification: a retry moves this row forward
  -- rather than inserting a second one.
  UNIQUE(notification_id,channel_id)
);
CREATE INDEX notification_deliveries_state ON notification_deliveries(state,updated_at DESC);

-- Which channels a rule tells. A rule with no binding still records its hits;
-- this table decides whether anyone gets told about them.
CREATE TABLE rule_notification_channels (
  rule_id uuid NOT NULL REFERENCES detection_rules(id) ON DELETE CASCADE,
  channel_id uuid NOT NULL REFERENCES notification_channels(id) ON DELETE CASCADE,
  PRIMARY KEY(rule_id,channel_id)
);

-- Snapshot archiving: the bytes stop living only in the detector. Frigate keeps
-- its own short-lived snapshot directory; a copy lands in the channel's bound
-- pool so the picture survives the detector's retention (STO-07).
ALTER TABLE events ADD COLUMN snapshot_relative_path text;
ALTER TABLE events ADD COLUMN snapshot_pool_id uuid REFERENCES storage_pools(id);
-- Why a snapshot was not archived, when it was not: pool_unbound, no bindable
-- pool, or a fetch that the detector refused. Reported as-is instead of
-- implying the picture is safe.
ALTER TABLE events ADD COLUMN snapshot_archive_error text CHECK(snapshot_archive_error IS NULL OR length(snapshot_archive_error)<=64);
-- Local copies expire on their own clock, independently of the event index
-- (EVT-07: snapshots have their own retention).
ALTER TABLE events ADD COLUMN snapshot_archived_at timestamptz;
CREATE INDEX events_snapshot_pending ON events(created_at) WHERE snapshot_relative_path IS NULL AND snapshot_state='ready';
