-- Reverts 003_battle_dem_replays.up.sql.
--
-- Dropping the table discards every stored replay container; only run this
-- deliberately.

DROP INDEX IF EXISTS ix_battle_dem_replays_user_time;
DROP INDEX IF EXISTS ix_battle_dem_replays_replay;
DROP TABLE IF EXISTS battle_dem_replays;
