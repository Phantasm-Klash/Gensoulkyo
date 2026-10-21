-- 005: durable matches started from a room or from matchmaking.
--
-- core.Service.matches is the in-process map this replaces. A match stuck in
-- "loading" (a player who never sent match.ready) survived only until the
-- battle agent polled it 15 minutes later; the durable row gives the sweep a
-- place to record why it reaped the match.

CREATE TABLE IF NOT EXISTS lobby_matches (
    match_id               TEXT PRIMARY KEY,
    mode_id                TEXT NOT NULL,
    room_code              TEXT NOT NULL DEFAULT '',
    status                 TEXT NOT NULL DEFAULT 'loading',
    boss_id                TEXT NOT NULL DEFAULT '',
    stage_id               TEXT NOT NULL DEFAULT '',
    ruleset_version        TEXT NOT NULL DEFAULT '',
    mode_ruleset_version   TEXT NOT NULL DEFAULT '',
    reward_table_id        TEXT NOT NULL DEFAULT '',
    server_seed            BIGINT NOT NULL DEFAULT 0,
    tick                   INTEGER NOT NULL DEFAULT 0,
    boss_hp                INTEGER NOT NULL DEFAULT 0,
    boss_max_hp            INTEGER NOT NULL DEFAULT 0,
    player_count           INTEGER NOT NULL DEFAULT 0,
    battle_allocation_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    battle_result_hash     TEXT NOT NULL DEFAULT '',
    battle_result_replay   TEXT NOT NULL DEFAULT '',
    battle_result_key_id   TEXT NOT NULL DEFAULT '',
    battle_result_at       TIMESTAMPTZ,
    created_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    started_at             TIMESTAMPTZ,
    ended_at               TIMESTAMPTZ,
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at             TIMESTAMPTZ,
    CONSTRAINT lobby_matches_status_check CHECK (status IN ('loading', 'running', 'ended', 'abandoned', 'expired'))
);

CREATE INDEX IF NOT EXISTS ix_lobby_matches_status_updated
    ON lobby_matches (status, updated_at);
CREATE INDEX IF NOT EXISTS ix_lobby_matches_room
    ON lobby_matches (room_code);
CREATE INDEX IF NOT EXISTS ix_lobby_matches_expires
    ON lobby_matches (expires_at) WHERE status IN ('loading', 'running');

CREATE TABLE IF NOT EXISTS lobby_match_players (
    match_id           TEXT NOT NULL,
    user_id            TEXT NOT NULL,
    seat_index         INTEGER NOT NULL DEFAULT 0,
    display_name       TEXT NOT NULL DEFAULT '',
    boss_id            TEXT NOT NULL DEFAULT '',
    deck_snapshot_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    loadout_json       JSONB NOT NULL DEFAULT '{}'::jsonb,
    ready              BOOLEAN NOT NULL DEFAULT false,
    result             TEXT NOT NULL DEFAULT '',
    score              INTEGER NOT NULL DEFAULT 0,
    rank_delta         INTEGER NOT NULL DEFAULT 0,
    world_boss_damage  INTEGER NOT NULL DEFAULT 0,
    joined_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    settled_at         TIMESTAMPTZ,
    PRIMARY KEY (match_id, user_id)
);

CREATE INDEX IF NOT EXISTS ix_lobby_match_players_user
    ON lobby_match_players (user_id, joined_at DESC);
