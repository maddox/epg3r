-- A channel is a playlist line from a source, and its identity is that line's URL.
-- Nothing about the title, the league, or the slot label takes part: those are
-- re-resolved on every run and are free to change. The number is written once and only
-- ever changes when the user changes it.
CREATE TABLE channels (
    key        TEXT PRIMARY KEY,           -- hash of source id and URL
    source_id  INTEGER NOT NULL REFERENCES sources(id) ON DELETE CASCADE,
    url        TEXT NOT NULL,
    channel_id TEXT,                       -- the id given to consumers, assigned with the number
    number     INTEGER,                    -- NULL until a league is resolved for it
    by_user    INTEGER NOT NULL DEFAULT 0, -- the number was set by hand; never auto-touched
    first_seen TEXT NOT NULL,
    last_seen  TEXT NOT NULL,
    UNIQUE (source_id, url),
    UNIQUE (number),
    UNIQUE (channel_id)
);

-- Superseded: team feeds were keyed by their title, which the provider is free to edit.
DROP TABLE IF EXISTS channel_alloc;

-- Superseded: a channel's league comes from the catalog, and nothing in the app decides
-- what is exported, so a provider's groups are not something to hold state about.
DROP TABLE IF EXISTS groups;
