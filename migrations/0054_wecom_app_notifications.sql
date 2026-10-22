-- WeCom (企业微信) APPLICATION messages, alongside the group robot.
--
-- The two ways to put an alert into WeCom are genuinely different:
--
--   wecom      — a group robot. One webhook URL; posts into one group.
--   wecom_app  — an enterprise application. corpID + secret are exchanged for an
--                access token, and the message is addressed to named members or
--                departments. It can reach people who are not in any group, and
--                it survives a group being archived.
--
-- They authenticate differently and address differently, which is why they are two
-- kinds rather than one kind with a mode flag: the endpoint CHECK below would
-- otherwise have to express "a URL xor a corpID", and every reader of the row
-- would have to know which fields are live.
--
-- The application secret is stored in the same sealed secret column as every
-- other credential. corpID and agentID are identifiers, not secrets — they appear
-- in error messages the operator reads and in WeCom's own admin console.

-- The message-type column was constrained to the robot's two layouts. An
-- application has its own three (text, markdown, textcard), so the column widens
-- to the union: which values are legal is decided by the kind, and the endpoint
-- CHECK below already enforces that a wecom_app row has one.
ALTER TABLE notification_channels DROP CONSTRAINT notification_channels_wecom_msg_type_check;
ALTER TABLE notification_channels ADD CONSTRAINT notification_channels_wecom_msg_type_check
  CHECK (wecom_msg_type IS NULL OR wecom_msg_type IN ('markdown','news','text','textcard'));

ALTER TABLE notification_channels DROP CONSTRAINT notification_channels_kind_check;
ALTER TABLE notification_channels ADD CONSTRAINT notification_channels_kind_check
  CHECK (kind IN ('inapp','webhook','email','wecom','wecom_app'));

-- The enterprise ID ("企业ID"). Required for wecom_app.
ALTER TABLE notification_channels ADD COLUMN wecom_corp_id text
  CHECK (wecom_corp_id IS NULL OR length(wecom_corp_id) BETWEEN 1 AND 64);
-- The application's agent id ("应用ID"). Required for wecom_app.
ALTER TABLE notification_channels ADD COLUMN wecom_agent_id integer
  CHECK (wecom_agent_id IS NULL OR wecom_agent_id > 0);
-- Who receives it: user ids, department ids, tags — WeCom's own touser/toparty/totag
-- syntax, pipe separated as the API takes it. Kept as one text field because the
-- operator copies it from the app's "可见范围" page and does not want to translate
-- it into three boxes.
ALTER TABLE notification_channels ADD COLUMN wecom_to text
  CHECK (wecom_to IS NULL OR length(wecom_to) <= 512);

ALTER TABLE notification_channels DROP CONSTRAINT notification_channels_endpoint_check;
ALTER TABLE notification_channels ADD CONSTRAINT notification_channels_endpoint_check CHECK (
  (kind = 'inapp' AND url IS NULL)
  OR (kind = 'webhook' AND url IS NOT NULL)
  OR (kind = 'email' AND url IS NULL AND sender_address IS NOT NULL AND smtp_host IS NOT NULL AND smtp_port IS NOT NULL)
  OR (kind = 'wecom' AND url IS NOT NULL AND wecom_msg_type IS NOT NULL)
  OR (kind = 'wecom_app' AND url IS NULL AND wecom_corp_id IS NOT NULL
      AND wecom_agent_id IS NOT NULL AND wecom_to IS NOT NULL AND wecom_msg_type IS NOT NULL)
);
