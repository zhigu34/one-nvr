-- M3 batch 1: normalized detection events, handling trail, Frigate camera
-- mapping and the stream-gap ledger. PRD EVT-01/02/03/07, CH-07.

CREATE TABLE frigate_cameras (
  channel_id uuid PRIMARY KEY REFERENCES channels(id),
  frigate_name text NOT NULL CHECK(length(frigate_name) BETWEEN 1 AND 64),
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE(frigate_name)
);

CREATE TABLE events (
  id uuid PRIMARY KEY,
  channel_id uuid REFERENCES channels(id),
  source_revision_id uuid,
  source text NOT NULL CHECK(source IN ('frigate')),
  source_id text NOT NULL CHECK(length(source_id) BETWEEN 1 AND 128),
  category text NOT NULL CHECK(length(category) BETWEEN 1 AND 64),
  score real NOT NULL DEFAULT 0,
  zones jsonb NOT NULL DEFAULT '[]',
  started_at timestamptz NOT NULL,
  ended_at timestamptz,
  snapshot_state text NOT NULL DEFAULT 'pending' CHECK(snapshot_state IN ('pending','ready','gone')),
  media_state text NOT NULL DEFAULT 'pending' CHECK(media_state IN ('pending','linked','unavailable')),
  handle_state text NOT NULL DEFAULT 'open' CHECK(handle_state IN ('open','acknowledged','closed','false_positive')),
  handle_note text NOT NULL DEFAULT '',
  version bigint NOT NULL DEFAULT 1 CHECK(version>0),
  raw jsonb NOT NULL DEFAULT '{}',
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  expires_at timestamptz,
  UNIQUE(source,source_id)
);
CREATE INDEX events_recent ON events(started_at DESC,id DESC);
CREATE INDEX events_channel ON events(channel_id,started_at DESC);
CREATE INDEX events_expiry ON events(expires_at) WHERE expires_at IS NOT NULL;

CREATE TABLE event_handling (
  id uuid PRIMARY KEY,
  event_id uuid NOT NULL REFERENCES events(id) ON DELETE CASCADE,
  actor_id uuid NOT NULL REFERENCES users(id),
  actor_name text NOT NULL,
  state text NOT NULL CHECK(state IN ('open','acknowledged','closed','false_positive')),
  note text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX event_handling_event ON event_handling(event_id,created_at);

-- EVT-04 seed: a subscription outage is a first-class record, not silence.
CREATE TABLE event_stream_gaps (
  id uuid PRIMARY KEY,
  started_at timestamptz NOT NULL,
  ended_at timestamptz,
  reason text NOT NULL CHECK(length(reason) BETWEEN 1 AND 64),
  created_at timestamptz NOT NULL DEFAULT now()
);
