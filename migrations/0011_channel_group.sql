-- Channel grouping is a business attribute: renaming or regrouping a channel
-- must never rebuild its media stream (PRD CH-01/CH-03). The default keeps
-- every existing row valid without a backfill, and grouping carries no media or
-- permission meaning (PRD CH-02 groups and policy assignment are orthogonal).
ALTER TABLE channels ADD COLUMN channel_group text NOT NULL DEFAULT ''
 CHECK(char_length(channel_group)<=64 AND channel_group !~ '[[:cntrl:]]');
