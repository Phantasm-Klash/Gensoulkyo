-- 006: live battle ticket state + request idempotency.
--
-- `battle_ticket_audits` (001) is an append-only audit trail; this is the
-- mutable state the lobby needs to consume a ticket exactly once across a
-- restart. `idempotency_keys` gives the write paths (chest opening, gacha pull,
-- shop purchase, battle result callback) a durable exactly-once marker.

CREATE TABLE IF NOT EXISTS battle_tickets (
    ticket_id            TEXT PRIMARY KEY,
    match_id             TEXT NOT NULL,
    user_id              TEXT NOT NULL,
    player_id            TEXT NOT NULL DEFAULT '',
    mode_id              TEXT NOT NULL DEFAULT '',
    battle_server_id     TEXT NOT NULL DEFAULT '',
    endpoint             TEXT NOT NULL DEFAULT '',
    key_id               TEXT NOT NULL DEFAULT '',
    signature_hex        TEXT NOT NULL DEFAULT '',
    nonce_hex            TEXT NOT NULL DEFAULT '',
    ruleset_version      TEXT NOT NULL DEFAULT '',
    protocol_version     TEXT NOT NULL DEFAULT '',
    business_api_version TEXT NOT NULL DEFAULT '',
    battle_api_version   TEXT NOT NULL DEFAULT '',
    mode_config_hash     TEXT NOT NULL DEFAULT '',
    deck_snapshot_hash   TEXT NOT NULL DEFAULT '',
    ticket_json          JSONB NOT NULL DEFAULT '{}'::jsonb,
    status               TEXT NOT NULL DEFAULT 'issued',
    reject_reason        TEXT NOT NULL DEFAULT '',
    issued_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at           TIMESTAMPTZ NOT NULL,
    consumed_at          TIMESTAMPTZ,
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    server_authoritative BOOLEAN NOT NULL DEFAULT true,
    CONSTRAINT battle_tickets_status_check CHECK (status IN ('issued', 'consumed', 'expired', 'revoked', 'rejected'))
);

CREATE INDEX IF NOT EXISTS ix_battle_tickets_match_status
    ON battle_tickets (match_id, status);
CREATE INDEX IF NOT EXISTS ix_battle_tickets_user
    ON battle_tickets (user_id, issued_at DESC);
CREATE INDEX IF NOT EXISTS ix_battle_tickets_expires
    ON battle_tickets (expires_at) WHERE status = 'issued';
-- Exactly one live ticket per (match, player): re-issuing is only possible once
-- the previous row has moved out of 'issued'.
CREATE UNIQUE INDEX IF NOT EXISTS uq_battle_tickets_active_player
    ON battle_tickets (match_id, user_id) WHERE status = 'issued';

CREATE TABLE IF NOT EXISTS idempotency_keys (
    scope                TEXT NOT NULL,
    idempotency_key      TEXT NOT NULL,
    request_fingerprint  TEXT NOT NULL DEFAULT '',
    response_json        JSONB NOT NULL DEFAULT '{}'::jsonb,
    status               TEXT NOT NULL DEFAULT 'in_progress',
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at           TIMESTAMPTZ NOT NULL,
    completed_at         TIMESTAMPTZ,
    PRIMARY KEY (scope, idempotency_key),
    CONSTRAINT idempotency_keys_status_check CHECK (status IN ('in_progress', 'completed'))
);

CREATE INDEX IF NOT EXISTS ix_idempotency_keys_expires
    ON idempotency_keys (expires_at);
