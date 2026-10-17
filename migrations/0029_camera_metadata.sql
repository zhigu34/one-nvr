-- Camera device metadata: brand, model, and physical type.
--
-- These are operator-facing facts about the hardware, not connection settings:
-- they describe what the camera *is*, while ip/paths/credentials describe how to
-- reach it. Storing them on the revision (rather than on the channel) keeps the
-- existing invariant that a channel's identity is fixed while its source
-- history accumulates — replacing a camera writes a new revision and the old
-- row keeps describing the old hardware.
--
-- All three are free text with a closed suggestion list in the UI, not an enum:
-- camera vendors ship models faster than this schema can be migrated, and a
-- mislabelled camera is a display problem, not a security one. Empty means "not
-- recorded", never "unknown brand", which is why the columns default to '' rather
-- than NULL: there is exactly one way to say nothing.
ALTER TABLE source_revisions
  ADD COLUMN brand text NOT NULL DEFAULT '' CHECK (char_length(brand) <= 64),
  ADD COLUMN model text NOT NULL DEFAULT '' CHECK (char_length(model) <= 128),
  -- 'bullet' 枪机, 'dome' 半球, 'ptz' 球机/云台, 'fisheye' 全景/鱼眼,
  -- 'multi' 多目, 'nvr' NVR 通道, '' not recorded.
  ADD COLUMN camera_type text NOT NULL DEFAULT ''
    CHECK (camera_type IN ('', 'bullet', 'dome', 'ptz', 'fisheye', 'multi', 'nvr'));

-- The comparison is on the applied revision, so the channel summary joins it.
-- Without this index the summary aggregates over every revision a channel has
-- ever had, which grows with each source change.
CREATE INDEX source_revisions_channel_identity ON source_revisions(channel_id, revision_no DESC);
