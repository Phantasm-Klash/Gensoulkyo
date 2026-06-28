-- Gensoulkyo business-security migration draft.
-- This schema is intentionally transport-neutral: HTTP fallback, Nakama RPC,
-- and business WSS should all write the same audit tables after the storage
-- layer is introduced.

CREATE TABLE IF NOT EXISTS business_envelope_keys (
    key_id TEXT PRIMARY KEY,
    protocol_version TEXT NOT NULL,
    suite TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('dev_scaffold', 'active', 'retired', 'revoked')),
    public_key_hex TEXT,
    server_key_ref TEXT,
    not_before TIMESTAMPTZ NOT NULL DEFAULT now(),
    not_after TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    rotated_at TIMESTAMPTZ,
    notes TEXT
);

CREATE TABLE IF NOT EXISTS business_envelope_audits (
    audit_id BIGSERIAL PRIMARY KEY,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    session_id_hint TEXT NOT NULL,
    user_id TEXT,
    transport TEXT NOT NULL CHECK (transport IN ('http_fallback', 'nakama_rpc', 'nakama_wss')),
    endpoint TEXT NOT NULL,
    op_code TEXT NOT NULL,
    key_id TEXT NOT NULL,
    protocol_version TEXT NOT NULL,
    seq BIGINT NOT NULL,
    nonce TEXT NOT NULL,
    request_timestamp_ms BIGINT NOT NULL,
    server_timestamp_ms BIGINT NOT NULL,
    accepted BOOLEAN NOT NULL,
    error_code TEXT,
    error_reason TEXT,
    replay BOOLEAN NOT NULL DEFAULT FALSE,
    body_hash TEXT,
    auth_tag_prefix TEXT,
    client_ip_hash TEXT,
    user_agent_hash TEXT,
    metadata_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    CONSTRAINT fk_business_envelope_audit_key
        FOREIGN KEY (key_id) REFERENCES business_envelope_keys(key_id)
        DEFERRABLE INITIALLY DEFERRED
);

CREATE UNIQUE INDEX IF NOT EXISTS ux_business_envelope_audit_session_seq
    ON business_envelope_audits (session_id_hint, seq)
    WHERE accepted;

CREATE UNIQUE INDEX IF NOT EXISTS ux_business_envelope_audit_session_nonce
    ON business_envelope_audits (session_id_hint, nonce)
    WHERE accepted;

CREATE INDEX IF NOT EXISTS ix_business_envelope_audit_user_time
    ON business_envelope_audits (user_id, created_at DESC);

CREATE INDEX IF NOT EXISTS ix_business_envelope_audit_rejected
    ON business_envelope_audits (accepted, error_code, created_at DESC);

CREATE TABLE IF NOT EXISTS business_envelope_nonce_windows (
    session_id_hint TEXT PRIMARY KEY,
    last_seq BIGINT NOT NULL DEFAULT 0,
    last_nonce TEXT,
    nonce_window_json JSONB NOT NULL DEFAULT '[]'::jsonb,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS battle_ticket_audits (
    ticket_id TEXT PRIMARY KEY,
    issued_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ NOT NULL,
    user_id TEXT NOT NULL,
    match_id TEXT NOT NULL,
    player_id TEXT NOT NULL,
    battle_server_id TEXT NOT NULL,
    endpoint TEXT NOT NULL,
    key_id TEXT NOT NULL,
    ruleset_version TEXT NOT NULL,
    protocol_version TEXT NOT NULL,
    deck_snapshot_hash TEXT NOT NULL,
    mode_config_hash TEXT NOT NULL,
    nonce TEXT NOT NULL,
    signature_prefix TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('issued', 'consumed', 'expired', 'revoked')) DEFAULT 'issued',
    consumed_at TIMESTAMPTZ,
    CONSTRAINT fk_battle_ticket_key
        FOREIGN KEY (key_id) REFERENCES business_envelope_keys(key_id)
        DEFERRABLE INITIALLY DEFERRED
);

CREATE INDEX IF NOT EXISTS ix_battle_ticket_audit_match
    ON battle_ticket_audits (match_id, user_id);

CREATE INDEX IF NOT EXISTS ix_battle_ticket_audit_status
    ON battle_ticket_audits (status, expires_at);

CREATE TABLE IF NOT EXISTS match_allocation_audits (
    allocation_id BIGSERIAL PRIMARY KEY,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    match_id TEXT NOT NULL,
    mode_id TEXT NOT NULL,
    battle_server_id TEXT NOT NULL,
    endpoint TEXT NOT NULL,
    region TEXT NOT NULL,
    protocol_version TEXT NOT NULL,
    ruleset_version TEXT NOT NULL,
    mode_config_hash TEXT NOT NULL,
    server_seed_hash TEXT NOT NULL,
    player_count INTEGER NOT NULL,
    allocation_json JSONB NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('allocated', 'started', 'settled', 'cancelled')) DEFAULT 'allocated'
);

CREATE UNIQUE INDEX IF NOT EXISTS ux_match_allocation_audit_match
    ON match_allocation_audits (match_id);

CREATE INDEX IF NOT EXISTS ix_match_allocation_audit_server_time
    ON match_allocation_audits (battle_server_id, created_at DESC);

INSERT INTO business_envelope_keys (
    key_id,
    protocol_version,
    suite,
    status,
    public_key_hex,
    server_key_ref,
    notes
) VALUES (
    'dev-business-envelope-v0',
    'business-v0-scaffold',
    'tls13_plus_x25519_hkdf_chacha20poly1305_ed25519_target',
    'dev_scaffold',
    NULL,
    'local-dev-placeholder',
    'Development scaffold key id used by HTTP fallback tests. Replace before production.'
) ON CONFLICT (key_id) DO NOTHING;
