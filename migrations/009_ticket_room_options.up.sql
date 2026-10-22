-- 009: room-level options on the lobby queue ticket.
--
-- A room's options (boss, stage, seat count and, lately, the match length) are
-- room-level: the host picks them, and every seat in the room runs under them.
-- The ticket is what the match snapshot is assembled from, so the ticket has to
-- carry them -- 008 gave the ticket a row but no place for options, which meant
-- an option added to a ticket field lived in memory only and vanished on
-- restart.
--
-- One jsonb column rather than a column per option: options are a small,
-- open-ended set, and this keeps adding one from needing another migration.
-- Missing or null means "no options set", not "failed to load".

ALTER TABLE queue_tickets
    ADD COLUMN IF NOT EXISTS mode_params jsonb;
