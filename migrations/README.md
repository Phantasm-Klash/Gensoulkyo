# migrations

PostgreSQL migration drafts for the planned Nakama/Go business server.

Current files:

- `001_business_security_audit.up.sql`: creates `business_envelope_keys`, `business_envelope_audits`, `packet_key_versions`, `business_envelope_nonce_windows`, `battle_servers`, `battle_ticket_audits`, `match_allocation_audits`, `battle_result_audits`, `replay_audits`, `lobby_room_audits`, and `lobby_message_audits`.
- `001_business_security_audit.down.sql`: rollback for the same draft tables.
- `002_accounts.up.sql` / `.down.sql`: e-mail + password account credentials (`gensoulkyo_accounts`).
- `003_battle_dem_replays.up.sql` / `.down.sql`: finished `.dem` v1 battle replay containers keyed by `match_id` (`battle_dem_replays`).
- `004_lobby_rooms.up.sql` / `.down.sql`: durable lobby room state — `lobby_rooms` (room code, mode, host, status, boss/stage, coop player count, the match it produced, mode params, chat messages, ticket ids, sweep expiry) and `lobby_room_members` (one row per seated player with deck snapshot + loadout, `left_at` for departures). Replaces the in-process `core.Service.rooms` map.
- `005_lobby_matches.up.sql` / `.down.sql`: durable match state — `lobby_matches` (mode, room it came from, status `loading|running|ended|abandoned|expired`, ruleset/reward-table stamps, server seed, tick, boss HP, battle allocation/result stamps, sweep expiry) and `lobby_match_players` (per-player seat, deck snapshot, loadout, readiness, result, score, rank delta, world boss damage, settlement time). Replaces `core.Service.matches`.
- `006_battle_tickets.up.sql` / `.down.sql`: `battle_tickets` — the *mutable* ticket state (status `issued|consumed|expired|revoked|rejected`, nonce, signature, expiry, consumed_at) that lets a restarted lobby still consume a ticket exactly once; a partial unique index on `(match_id, user_id) WHERE status = 'issued'` blocks double issue. `battle_ticket_audits` from 001 remains the append-only audit trail for the same lifecycle. Also creates `idempotency_keys` (`scope`, `idempotency_key`, request fingerprint + cached response) so write paths can be exactly-once across restarts.
- `007_world_boss_leaderboards.up.sql` / `.down.sql`: `world_boss_cycles` (current boss instance: HP, season, window, defeat stamps, announcement flag), `world_boss_contributions` (per user per match damage/attempts, `applied` guards double application) and `leaderboard_entries` (leaderboard + season + user score/rank). Replaces `core.Service.worldBoss`, `worldBossAttempts` and `userState.Leaderboards`.

Still pending (deliberately not in this batch — they follow the same pattern
once the room/match/ticket stores are wired in): player inventory, decks, chests
and chest pity, gacha pity, activity/event progress and reward claims.

These migrations define the persistence target for `runtime/security` audit events, Nakama RPC/WSS envelope validation, packet/key-version rotation metadata, battle server registry state plus registration/heartbeat/offline lifecycle rows, battle ticket issuance/consumption/rejected-consume audits and match allocation bookkeeping with protocol/business/battle/ruleset version stamps, accepted, duplicate, and rejected battle result callback audits, replay audit records, and lobby room/message lifecycle audit records.

`runtime/security.NewSQLBusinessEnvelopeAuditSink` already provides a standard-library `database/sql` writer for `business_envelope_audits`. `runtime/core` provides standard-library `database/sql` writers for battle server lifecycle/allocation/ticket/result/replay audits and lobby room/message lifecycle audits. `runtime/storage` can open an explicitly configured database and apply pending `.up.sql` files in version order. `cmd/gensoulkyo` registers the pgx driver as `pgx`; broader repository wiring and deployment automation are still pending.

## Databases

The migrations are applied identically to two databases inside the single
existing Postgres container (`gensoulkyo_nakama_postgres`):

| database | role | purpose |
| --- | --- | --- |
| `nakama_prod` | `gensoulkyo_prod` | production, seeded from a dump of the legacy `nakama` database |
| `nakama_test` | `gensoulkyo_test` | schema-only copy, no business data |

Each role has `CONNECT` on exactly one of them and `PUBLIC` connect is revoked,
so a misconfigured connection string fails loudly instead of writing test data
into production. Use `storage.PostgresConfig(storage.EnvironmentProd)` /
`storage.PostgresConfig(storage.EnvironmentTest)` rather than hand-written DSNs;
see `runtime/storage/env.go`.
