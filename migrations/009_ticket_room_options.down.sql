-- 009 down: drop the room-options column added above.

ALTER TABLE queue_tickets
    DROP COLUMN IF EXISTS mode_params;
