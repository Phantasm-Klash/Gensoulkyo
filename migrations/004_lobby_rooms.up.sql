-- 004: durable lobby rooms.
--
-- Before this migration the authoritative room state lived only in
-- core.Service.rooms (an in-process map), so a lobby restart dropped every open
-- room and a room left at status "found" was never reaped. These tables are the
-- durable copy the store layer writes through to; Redis keeps the hot mirror
-- (see runtime/storage/redis.go) and Postgres remains the system of record.

CREATE TABLE IF NOT EXISTS lobby_rooms (
    room_code        TEXT PRIMARY KEY,
    mode_id          TEXT NOT NULL,
    host_user_id     TEXT NOT NULL,
    status           TEXT NOT NULL DEFAULT 'waiting',
    boss_id          TEXT NOT NULL DEFAULT '',
    stage_id         TEXT NOT NULL DEFAULT '',
    coop_players     INTEGER NOT NULL DEFAULT 0,
    match_id         TEXT NOT NULL DEFAULT '',
    mode_params_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    messages_json    JSONB NOT NULL DEFAULT '[]'::jsonb,
    ticket_ids_json  JSONB NOT NULL DEFAULT '[]'::jsonb,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at       TIMESTAMPTZ,
    closed_at        TIMESTAMPTZ,
    CONSTRAINT lobby_rooms_status_check CHECK (status IN ('waiting', 'found', 'cancelled', 'closed', 'expired'))
);

CREATE INDEX IF NOT EXISTS ix_lobby_rooms_status_updated
    ON lobby_rooms (status, updated_at);
CREATE INDEX IF NOT EXISTS ix_lobby_rooms_host
    ON lobby_rooms (host_user_id);
-- Drives the zombie sweep: rooms whose expires_at has passed but that were
-- never explicitly closed.
CREATE INDEX IF NOT EXISTS ix_lobby_rooms_expires
    ON lobby_rooms (expires_at) WHERE status IN ('waiting', 'found');

CREATE TABLE IF NOT EXISTS lobby_room_members (
    room_code          TEXT NOT NULL,
    user_id            TEXT NOT NULL,
    ticket_id          TEXT NOT NULL DEFAULT '',
    seat_index         INTEGER NOT NULL DEFAULT 0,
    is_host            BOOLEAN NOT NULL DEFAULT false,
    deck_snapshot_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    loadout_json       JSONB NOT NULL DEFAULT '{}'::jsonb,
    boss_id            TEXT NOT NULL DEFAULT '',
    joined_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    left_at            TIMESTAMPTZ,
    PRIMARY KEY (room_code, user_id)
);

CREATE INDEX IF NOT EXISTS ix_lobby_room_members_ticket
    ON lobby_room_members (ticket_id);
CREATE INDEX IF NOT EXISTS ix_lobby_room_members_user
    ON lobby_room_members (user_id);
