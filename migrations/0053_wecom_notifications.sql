-- WeCom (企业微信) group robot as a notification channel kind. EVT-05.
--
-- A group robot is one webhook URL and nothing else: the operator adds a robot to
-- the duty group, copies the URL, and every alert lands there. That is the shape
-- an NVR operator expects, and it needs no enterprise-admin application, no
-- corpsecret, and no access-token lifecycle — which is why this is the kind
-- shipped rather than an app-message channel.
--
-- The URL carries the robot key as a query parameter (`?key=...`), so it is a
-- credential in the same sense a webhook URL is: it is the thing that lets
-- anyone post into the group. It is stored in `url` like a webhook's, and the
-- API never echoes it into an audit row (safejson refuses `url` outright).

ALTER TABLE notification_channels DROP CONSTRAINT notification_channels_kind_check;
ALTER TABLE notification_channels ADD CONSTRAINT notification_channels_kind_check
  CHECK (kind IN ('inapp','webhook','email','wecom'));

-- How the message is laid out in the group.
--
--   markdown — one text block; fits the most information into a chat line and
--              keeps working when a snapshot is unavailable.
--   news     — a card with a title and a link. The link is what makes an alert
--              actionable in a chat app, but only one card per message.
ALTER TABLE notification_channels ADD COLUMN wecom_msg_type text
  CHECK (wecom_msg_type IS NULL OR wecom_msg_type IN ('markdown','news'));

-- Phone numbers to @mention, comma separated. Empty means no mention: a robot
-- that pings the whole duty group on every motion event gets muted.
ALTER TABLE notification_channels ADD COLUMN wecom_mentions text
  CHECK (wecom_mentions IS NULL OR length(wecom_mentions) <= 512);

ALTER TABLE notification_channels DROP CONSTRAINT notification_channels_endpoint_check;
ALTER TABLE notification_channels ADD CONSTRAINT notification_channels_endpoint_check CHECK (
  (kind = 'inapp' AND url IS NULL)
  OR (kind = 'webhook' AND url IS NOT NULL)
  OR (kind = 'email' AND url IS NULL AND sender_address IS NOT NULL AND smtp_host IS NOT NULL AND smtp_port IS NOT NULL)
  OR (kind = 'wecom' AND url IS NOT NULL AND wecom_msg_type IS NOT NULL)
);
