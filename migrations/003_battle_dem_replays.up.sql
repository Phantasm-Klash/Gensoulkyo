-- Gensoulkyo battle replay (.dem v1) container store — thread B.
--
-- One row per finished match, keyed by match_id (the battle server uploads a
-- whole .dem container in base64 chunks and the lobby assembles + verifies it).
-- replay_id is carried as metadata because the upload body names both; the
-- download route accepts either the replay_id or the replay's match_id.
--
-- The container is already zlib-compressed on the wire (COORDINATION §0.2), so
-- bytes are stored verbatim as BYTEA — no second compression.

CREATE TABLE IF NOT EXISTS battle_dem_replays (
    match_id TEXT PRIMARY KEY,
    replay_id TEXT NOT NULL DEFAULT '',
    user_id TEXT NOT NULL DEFAULT '',
    mode_id TEXT NOT NULL DEFAULT '',
    boss_id TEXT NOT NULL DEFAULT '',
    sha256 TEXT NOT NULL,
    size BIGINT NOT NULL DEFAULT 0,
    entry_count BIGINT NOT NULL DEFAULT 0,
    final_tick BIGINT NOT NULL DEFAULT 0,
    final_state_hash TEXT NOT NULL DEFAULT '',
    server_seed TEXT NOT NULL DEFAULT '',
    tick_rate_hz INTEGER NOT NULL DEFAULT 0,
    dem_bytes BYTEA NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    server_authoritative BOOLEAN NOT NULL DEFAULT TRUE
);

CREATE INDEX IF NOT EXISTS ix_battle_dem_replays_replay
    ON battle_dem_replays (replay_id);

CREATE INDEX IF NOT EXISTS ix_battle_dem_replays_user_time
    ON battle_dem_replays (user_id, created_at DESC);
