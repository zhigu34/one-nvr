-- Detection sensitivity: how readily the detector calls something a detection.
-- PRD AI-01/AI-06.
--
-- The rendered Frigate config carries no `filters` block at all, so every camera
-- runs on Frigate's defaults (min_score 0.5). On a driveway with a parked
-- scooter and a car park in frame that threshold both misses slow, partial
-- movement and fires on parked objects — the operator's report was "误告多" and,
-- separately, "轻微移动抓不到".
--
-- One enum instead of three numbers: min_score, threshold and min_area have to
-- move together, and exposing them separately invites a combination that either
-- never fires or fires on everything. The three profiles are declared in Go
-- (internal/detection/sensitivity.go) and this column only names one.
--
-- Existing rows default to 'balanced', which reproduces today's behaviour for
-- rows that were already configured: an upgrade must not silently change what a
-- camera detects.
ALTER TABLE detection_configs
  ADD COLUMN sensitivity text NOT NULL DEFAULT 'balanced'
  CHECK (sensitivity IN ('sensitive', 'balanced', 'strict'));
