# migrations

PostgreSQL migrations for the open-source Gensoulkyo server.

- `000001_initial_schema.up.sql` mirrors the server/database/economy schema from the architecture docs.
- `000001_initial_schema.down.sql` drops the same schema in reverse dependency order.
- The initial up migration also seeds the open-alpha card catalog and a free local starter chest pool for self-hosted development.
- `match_reward_settlements` enforces one local reward settlement per `(match_id, user_id)` so repeated completion paths cannot double-credit the wallet.
