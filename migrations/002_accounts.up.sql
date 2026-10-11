-- Gensoulkyo e-mail + password accounts.
--
-- One row per self-service (or operator-seeded) account. The lobby keeps live
-- sessions in memory, but credentials must survive a restart, so they live
-- here. Both the e-mail and the username are stored twice: the display form the
-- player typed, and a normalised (lower-cased, trimmed) form that carries the
-- unique index. That makes "Wjcwqc" and "wjcwqc" the same account without
-- losing the casing the player chose.

CREATE TABLE IF NOT EXISTS gensoulkyo_accounts (
    account_id TEXT PRIMARY KEY,
    username TEXT NOT NULL,
    username_normalized TEXT NOT NULL,
    email TEXT NOT NULL,
    email_normalized TEXT NOT NULL,
    display_name TEXT NOT NULL DEFAULT '',
    -- Self-describing PBKDF2-HMAC-SHA256 hash: pbkdf2-sha256$<iter>$<salt>$<key>.
    password_hash TEXT NOT NULL,
    email_verified BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_login_at TIMESTAMPTZ
);

CREATE UNIQUE INDEX IF NOT EXISTS ux_gensoulkyo_accounts_email
    ON gensoulkyo_accounts (email_normalized);

CREATE UNIQUE INDEX IF NOT EXISTS ux_gensoulkyo_accounts_username
    ON gensoulkyo_accounts (username_normalized);

CREATE INDEX IF NOT EXISTS ix_gensoulkyo_accounts_created
    ON gensoulkyo_accounts (created_at);
