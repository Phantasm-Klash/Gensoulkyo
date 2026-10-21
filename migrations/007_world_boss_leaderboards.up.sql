-- 007: world boss cycle state, per-player boss damage and leaderboard rows.
--
-- core.Service.worldBoss / worldBossAttempts and userState.Leaderboards are the
-- in-process maps these tables replace. Losing them on restart silently resets
-- a running boss and every seasonal score.

CREATE TABLE IF NOT EXISTS world_boss_cycles (
    boss_instance_id      TEXT PRIMARY KEY,
    season_id             TEXT NOT NULL DEFAULT '',
    boss_id               TEXT NOT NULL DEFAULT '',
    status                TEXT NOT NULL DEFAULT 'active',
    max_hp                INTEGER NOT NULL DEFAULT 0,
    current_hp            INTEGER NOT NULL DEFAULT 0,
    starts_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    ends_at               TIMESTAMPTZ,
    defeated_at           TIMESTAMPTZ,
    defeated_by_match_id  TEXT NOT NULL DEFAULT '',
    defeated_by_user_id   TEXT NOT NULL DEFAULT '',
    announcement_emitted  BOOLEAN NOT NULL DEFAULT false,
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT world_boss_cycles_status_check CHECK (status IN ('active', 'defeated', 'expired'))
);

CREATE INDEX IF NOT EXISTS ix_world_boss_cycles_status
    ON world_boss_cycles (status, ends_at);

CREATE TABLE IF NOT EXISTS world_boss_contributions (
    boss_instance_id TEXT NOT NULL,
    user_id          TEXT NOT NULL,
    match_id         TEXT NOT NULL DEFAULT '',
    damage           INTEGER NOT NULL DEFAULT 0,
    attempts         INTEGER NOT NULL DEFAULT 0,
    applied          BOOLEAN NOT NULL DEFAULT false,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (boss_instance_id, user_id, match_id)
);

CREATE INDEX IF NOT EXISTS ix_world_boss_contributions_user
    ON world_boss_contributions (boss_instance_id, damage DESC);

CREATE TABLE IF NOT EXISTS leaderboard_entries (
    leaderboard_id TEXT NOT NULL,
    season_id      TEXT NOT NULL DEFAULT '',
    user_id        TEXT NOT NULL,
    score          BIGINT NOT NULL DEFAULT 0,
    rank_position  INTEGER NOT NULL DEFAULT 0,
    payload_json   JSONB NOT NULL DEFAULT '{}'::jsonb,
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (leaderboard_id, season_id, user_id)
);

CREATE INDEX IF NOT EXISTS ix_leaderboard_entries_score
    ON leaderboard_entries (leaderboard_id, season_id, score DESC, user_id);
