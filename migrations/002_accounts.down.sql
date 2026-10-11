-- Reverts 002_accounts.up.sql.
--
-- Dropping the table destroys every registered credential, so this is only ever
-- run deliberately by an operator.

DROP INDEX IF EXISTS ix_gensoulkyo_accounts_created;
DROP INDEX IF EXISTS ux_gensoulkyo_accounts_username;
DROP INDEX IF EXISTS ux_gensoulkyo_accounts_email;
DROP TABLE IF EXISTS gensoulkyo_accounts;
