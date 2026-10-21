-- SMTP email as a notification channel kind (EVT-05).
--
-- The channel table was built for endpoints: `url` plus one sealed secret. An SMTP
-- channel needs a different shape — a server, a port, a TLS choice, a sender
-- identity, an authentication switch, and a set of recipients — so rather than
-- stretching `url` into something it is not, the parameters live in their own
-- columns and the sealed secret keeps holding only the password.
--
-- Recipients are a table, not a JSON array: each one has its own test button in
-- the operator's mental model ("测试" per row), and a recipient that bounces is a
-- fact about that address, not about the channel.

ALTER TABLE notification_channels DROP CONSTRAINT notification_channels_kind_check;
ALTER TABLE notification_channels ADD CONSTRAINT notification_channels_kind_check
  CHECK (kind IN ('inapp','webhook','email'));

-- The sender's display name ("发件人名称"). Separate from the address so a
-- mailbox can say 门岗值班 while the envelope says no-reply@site.
ALTER TABLE notification_channels ADD COLUMN sender_name text
  CHECK (sender_name IS NULL OR length(sender_name) <= 128);
-- The From address. Required for email, meaningless for the other kinds.
ALTER TABLE notification_channels ADD COLUMN sender_address text
  CHECK (sender_address IS NULL OR (length(sender_address) >= 3 AND length(sender_address) <= 254));
ALTER TABLE notification_channels ADD COLUMN smtp_host text
  CHECK (smtp_host IS NULL OR (length(smtp_host) >= 1 AND length(smtp_host) <= 255));
ALTER TABLE notification_channels ADD COLUMN smtp_port integer
  CHECK (smtp_port IS NULL OR (smtp_port BETWEEN 1 AND 65535));
-- "启用SSL": implicit TLS (SMTPS, usually 465). STARTTLS is attempted whenever the
-- server advertises it and this is false, which is what a port-587 relay expects.
ALTER TABLE notification_channels ADD COLUMN smtp_tls boolean NOT NULL DEFAULT false;
-- "服务器认证": whether to send AUTH at all. A relay on the local network may need
-- none, and sending AUTH to a server that does not offer it is a failure.
ALTER TABLE notification_channels ADD COLUMN smtp_auth boolean NOT NULL DEFAULT true;
-- The AUTH username. Not a secret — the password is the sealed one — so it lives
-- in its own column rather than being packed into `url`.
ALTER TABLE notification_channels ADD COLUMN smtp_username text
  CHECK (smtp_username IS NULL OR length(smtp_username) <= 254);

-- The old constraint demanded url IS NULL for inapp and NOT NULL for webhook; it
-- has to learn about a third kind that carries neither.
ALTER TABLE notification_channels DROP CONSTRAINT notification_channels_check;
ALTER TABLE notification_channels ADD CONSTRAINT notification_channels_endpoint_check CHECK (
  (kind = 'inapp' AND url IS NULL)
  OR (kind = 'webhook' AND url IS NOT NULL)
  OR (kind = 'email' AND url IS NULL AND sender_address IS NOT NULL AND smtp_host IS NOT NULL AND smtp_port IS NOT NULL)
);

CREATE TABLE notification_recipients (
  id uuid PRIMARY KEY,
  channel_id uuid NOT NULL REFERENCES notification_channels(id) ON DELETE CASCADE,
  name text NOT NULL DEFAULT '' CHECK (length(name) <= 64),
  address text NOT NULL CHECK (length(address) BETWEEN 3 AND 254),
  -- Per-address test outcome, mirroring notification_channels.last_check: the
  -- operator sees which mailbox refused instead of a channel-level verdict.
  last_check jsonb,
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (channel_id, address)
);
CREATE INDEX notification_recipients_channel ON notification_recipients(channel_id, created_at);
