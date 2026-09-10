-- A collection is a set of channels the user has picked out, exported at its own URLs.
-- The guide as a whole is everything epg3r recognizes; a collection is what someone
-- actually wants a consumer to see, and only the user knows what that is.
CREATE TABLE collections (
    id         INTEGER PRIMARY KEY,
    name       TEXT NOT NULL,
    slug       TEXT NOT NULL,   -- what its URLs are addressed by
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE (slug)
);

-- Membership follows the channel, so a channel forgotten because its URL is gone leaves
-- the collections it was in rather than lingering as a name with nothing behind it.
CREATE TABLE collection_channels (
    collection_id INTEGER NOT NULL REFERENCES collections(id) ON DELETE CASCADE,
    channel_key   TEXT NOT NULL REFERENCES channels(key) ON DELETE CASCADE,
    added_at      TEXT NOT NULL,
    PRIMARY KEY (collection_id, channel_key)
);

CREATE INDEX collection_channels_by_channel ON collection_channels (channel_key);
