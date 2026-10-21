-- 008: lobby queue tickets.
--
-- battle_tickets (006) holds the *signed* ticket a battle server consumes once.
-- It does not hold the lobby's own queue ticket: the thing a player gets when
-- they join matchmaking or a room, which carries their seat, deck, loadout and
-- room binding. Those lived in the process map only, so after a restart a
-- player's seat was durable (lobby_room_members) but their ticket was not, and
-- a rejoin could not tell it was the same player coming back.
--
-- Status values mirror the lobby's own vocabulary: queued, found, cancelled.

CREATE TABLE IF NOT EXISTS queue_tickets (
    ticket_id            TEXT PRIMARY KEY,
    user_id              TEXT NOT NULL,
    mode_id              TEXT NOT NULL DEFAULT '',
    queue_key            TEXT NOT NULL DEFAULT '',
    room_code            TEXT NOT NULL DEFAULT '',
    room_status          TEXT NOT NULL DEFAULT '',
    match_id             TEXT NOT NULL DEFAULT '',
    status               TEXT NOT NULL DEFAULT 'queued',
    boss_id              TEXT NOT NULL DEFAULT '',
    coop_players         INTEGER NOT NULL DEFAULT 0,
    deck_snapshot_json   JSONB NOT NULL DEFAULT '{}'::jsonb,
    loadout_json         JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    server_authoritative BOOLEAN NOT NULL DEFAULT true,
    CONSTRAINT queue_tickets_status_check CHECK (status IN ('queued', 'found', 'cancelled'))
);

-- A player must not hold two live tickets: the join path asks this first.
CREATE UNIQUE INDEX IF NOT EXISTS ux_queue_tickets_live_user
    ON queue_tickets (user_id)
 WHERE status = 'queued';

CREATE INDEX IF NOT EXISTS ix_queue_tickets_room
    ON queue_tickets (room_code, status);
CREATE INDEX IF NOT EXISTS ix_queue_tickets_match
    ON queue_tickets (match_id, status);
CREATE INDEX IF NOT EXISTS ix_queue_tickets_status_updated
    ON queue_tickets (status, updated_at);
