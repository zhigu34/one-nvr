-- A publication is a snapshot of the per-channel detection settings. Recording
-- the versions it captured lets the page tell "the detector runs the stored
-- settings" from "settings changed since the last publication" without
-- depending on the media session, which the rendered input URL does depend on.
ALTER TABLE detection_config_revisions ADD COLUMN config_versions jsonb NOT NULL DEFAULT '{}';
