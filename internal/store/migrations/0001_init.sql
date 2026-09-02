-- Config tables hold what the user typed. Derived tables hold what runs computed.

CREATE TABLE settings (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE sources (
    id                 INTEGER PRIMARY KEY,
    name               TEXT NOT NULL,
    url                TEXT NOT NULL,
    xmltv_url          TEXT NOT NULL DEFAULT '',
    enabled            INTEGER NOT NULL DEFAULT 1,
    provider           TEXT NOT NULL DEFAULT '',
    timezone           TEXT NOT NULL DEFAULT '',
    date_order         TEXT NOT NULL,
    vs_order           TEXT NOT NULL,
    id_prefix          TEXT NOT NULL DEFAULT '',
    created_at         TEXT NOT NULL,
    updated_at         TEXT NOT NULL,
    -- derived
    last_fetched_at    TEXT,
    last_status        TEXT,
    last_error         TEXT,
    last_channel_count INTEGER
);

CREATE TABLE league_overrides (
    key        TEXT PRIMARY KEY,
    patch      TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE league_custom (
    key        TEXT PRIMARY KEY,
    doc        TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE team_overrides (
    league_key TEXT NOT NULL,
    team_key   TEXT NOT NULL,
    patch      TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (league_key, team_key)
);

CREATE TABLE groups (
    id                  INTEGER PRIMARY KEY,
    source_id           INTEGER NOT NULL REFERENCES sources(id) ON DELETE CASCADE,
    name                TEXT NOT NULL,
    enabled             INTEGER,            -- NULL = automatic (enabled iff it maps to a league)
    league_key          TEXT,               -- user override: force this group to a league
    -- derived
    first_seen_at       TEXT NOT NULL,
    last_seen_at        TEXT NOT NULL,
    channel_count       INTEGER NOT NULL DEFAULT 0,
    matched_league_key  TEXT,
    UNIQUE (source_id, name)
);

CREATE TABLE title_patterns (
    id         INTEGER PRIMARY KEY,
    name       TEXT NOT NULL,
    regex      TEXT NOT NULL,
    provider   TEXT,
    priority   INTEGER NOT NULL DEFAULT 50,
    enabled    INTEGER NOT NULL DEFAULT 1,
    notes      TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

-- Provider families within a league. Several providers in one playlist may all have an
-- "NFL 04"; each label style gets a sticky family index so their channels stay distinct
-- and keep their ids and numbers across runs.
CREATE TABLE slot_families (
    source_id   INTEGER NOT NULL REFERENCES sources(id) ON DELETE CASCADE,
    league_key  TEXT NOT NULL,
    label_shape TEXT NOT NULL,   -- how the slot label is written, e.g. "USA|NFL#:"
    sched_shape TEXT NOT NULL,   -- how the schedule is written, e.g. "#.##:#AA"; "" until a game is seen
    family      INTEGER NOT NULL,
    example     TEXT NOT NULL,
    created_at  TEXT NOT NULL,
    PRIMARY KEY (source_id, league_key, label_shape, sched_shape),
    UNIQUE (source_id, league_key, family)
);

-- Sticky channel identity for channels that have no slot number of their own (team
-- channels). Once a feed gets an id and number it keeps them across runs.
CREATE TABLE channel_alloc (
    source_id  INTEGER NOT NULL REFERENCES sources(id) ON DELETE CASCADE,
    league_key TEXT NOT NULL,
    feed_key   TEXT NOT NULL,   -- what the provider keeps stable for the feed, usually its title
    channel_id TEXT NOT NULL,
    number     INTEGER NOT NULL,
    created_at TEXT NOT NULL,
    PRIMARY KEY (source_id, league_key, feed_key),
    UNIQUE (channel_id),
    UNIQUE (number)
);

CREATE TABLE runs (
    id            INTEGER PRIMARY KEY,
    trigger       TEXT NOT NULL,      -- see store.Trigger
    started_at    TEXT NOT NULL,
    finished_at   TEXT,
    status        TEXT NOT NULL,      -- see store.RunStatus
    error         TEXT,
    counts_json   TEXT,               -- {outcome: count}, see store.Outcome
    snapshot_json BLOB
);

CREATE TABLE run_channels (
    id               INTEGER PRIMARY KEY,
    run_id           INTEGER NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    source_id        INTEGER,
    group_name       TEXT NOT NULL DEFAULT '',
    raw_title        TEXT NOT NULL,
    normalized_title TEXT NOT NULL DEFAULT '',
    tvg_id           TEXT NOT NULL DEFAULT '',
    tvg_name         TEXT NOT NULL DEFAULT '',
    tvg_logo         TEXT NOT NULL DEFAULT '',
    stream_url       TEXT NOT NULL DEFAULT '',
    status           TEXT NOT NULL,     -- see store.Outcome
    league_key       TEXT,
    kind             TEXT,              -- see model.ChannelKind
    channel_id       TEXT,
    channel_number   INTEGER,
    matchup          TEXT,
    team1            TEXT,
    team2            TEXT,
    start_at         TEXT,
    stop_at          TEXT,
    reason           TEXT,
    confidence       REAL
);
CREATE INDEX run_channels_run_status ON run_channels (run_id, status);
CREATE INDEX run_channels_run_league ON run_channels (run_id, league_key);

CREATE TABLE event_identity (
    channel_id    TEXT NOT NULL,
    local_date    TEXT NOT NULL,
    episode_id    TEXT NOT NULL,
    subtitle_norm TEXT NOT NULL,
    last_seen     TEXT NOT NULL,
    PRIMARY KEY (channel_id, local_date)
);
