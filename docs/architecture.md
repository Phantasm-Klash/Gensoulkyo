# Architecture

Gensoulkyo is planned as the authoritative server for Phantasm Klash online play.

The first implementation target is a self-hosted server that supports:

- player account creation and login without commercial platform dependencies;
- lobby and room creation;
- 1v1 match sessions;
- battle server allocation and signed battle ticket issuance;
- tick-based input ingestion;
- server-owned random seeds;
- validated card activation requests;
- match snapshots;
- replay metadata persistence.

The client submits intent. The server decides authoritative state.

## Current MVP

The first implementation is a Go standard-library service, split into:

- `runtime/core`: in-memory domain runtime for anonymous and external auth/session state, protocol version constants derived from the PhK-Protocol Go manifest, player inventory, card upgrades, saved decks, active deck resolution, chest pools/openings, queues, queue-ticket cancellation, room-code lobbies, server-validated deck/stage/character loadouts, heartbeat presence, battle server registration/heartbeat/allocation with mode-dedicated server preference, Ed25519-signed battle tickets, development signed-battle-result submission checks, matches, inputs, deterministic stage-aware bullet simulation, snapshots, PvP Duel mode-state/settlement contract, world-boss state, cursor-based event logs, settlements, rematch acceptance/new-match creation, replay audit records, wallet/task/event/leaderboard projections, and activity claims.
- `runtime/security`: transport-neutral business envelope scaffold guard for version/seq/timestamp/nonce/op/key/tag validation, replay rejection, sanitized audit snapshots, an audit sink interface, SDK-neutral `BusinessEnvelopeRequest` constructors for HTTP headers, Nakama RPC-style payloads, and Nakama WSS-style payloads, plus a standard-library `database/sql` sink adapter for `business_envelope_audits`.
- `runtime/storage`: optional database configuration and `.up.sql` migration runner. It keeps the local HTTP MVP in-memory by default and only opens a database when a URL is explicitly provided. `cmd/gensoulkyo` registers pgx as the default `database/sql` driver.
- `runtime/httpapi`: HTTP routes that expose the core service using the same payload shapes the Godot client prototype already models, including battle allocation, ticket fallback routes, battle result submit fallback route, optional `X-PhK-Business-*` envelope checks, and a development status endpoint for the envelope guard.
- `runtime/nakamaapi`: SDK-neutral Nakama Runtime adapter skeleton that maps external Nakama user/session ids into core sessions, dispatches key business RPCs and WSS-style business messages through `runtime/core`, requires the shared business envelope on authenticated calls, and records missing/invalid/replayed envelope audits through the same guard.
- `cmd/gensoulkyo_nakama`: optional `nakama` build-tagged Nakama Go Runtime binding source that registers RPC callbacks and forwards Nakama context/payload data into `runtime/nakamaapi`. The real tag build still needs an environment that can fetch or cache `github.com/heroiclabs/nakama-common/runtime`.
- `cmd/gensoulkyo`: local self-hosted entrypoint.

This is a stepping stone toward the planned Nakama + Go Runtime + PostgreSQL + C++ Battle Server stack. Keeping the domain logic independent makes it portable into Nakama RPC/WSS handlers while the current HTTP routes remain contract tests and local fallback.

The current business envelope guard is a migration scaffold, not production cryptography. It verifies the envelope field contract and replay window across one shared guard while real X25519/ECDHE, AEAD, and request signing are still pending. `migrations/001_business_security_audit.*.sql` defines the first PostgreSQL target for envelope keys, envelope audit rows, nonce-window checkpoints, battle ticket audits, and match allocation audits. The `database/sql` audit sink can write envelope audit rows once a real `*sql.DB` is provided, and `cmd/gensoulkyo` can explicitly run `.up.sql` migrations with `-migrate-up` through the pgx stdlib driver. Transport adapters should call `ValidateBusinessEnvelopeRequest` and then map the returned status/code/reason into their own protocol response.

## Authority Rules

The MVP enforces these boundaries:

- Deck saves are validated against the server-owned inventory, copy limits, rarity/interference limits, ranked bans, and ruleset version. Matchmaking resolves the active server deck into a match-only snapshot before queueing.
- Card upgrades are server authoritative. The client may request a card id and optional next level only; the server validates ownership, max level, target level, rarity-based dust cost, wallet deduction, and inventory level projection.
- Chest reads and openings are server authoritative. The server owns enabled pools, costs, weights, 10/60 pity counters, chest/key deduction, duplicate-to-dust conversion, card inventory updates, and opening audit records. Clients can request `pool_id`/`count` only and cannot submit chest results.
- Queue and room requests validate stage/character loadouts from `mode_params`. Open matchmaking is bucketed by mode and stage; room-code joins inherit or must match the room host's stage.
- Matched queues create a server-owned battle allocation with battle server id, endpoint, per-player battle ids, deck snapshot hashes, mode config hash, seed, and protocol version. Each participant can receive a short-lived Ed25519-signed battle ticket bound to match id, user id, player id, deck hash, ruleset, endpoint, and a non-login business session reference; the raw bearer session token is not embedded in the ticket.
- When multiple battle servers are healthy at the same computed load, allocation prefers the most mode-specific server over broad fallback servers. This keeps the local all-mode development server available while allowing a registered PvP/Boss C++ battle server to take its intended traffic.
- Battle result submission is a service-side contract, not a client-authoritative result path. The current development route validates `SignedBattleResult` version, match id, mode id, allocation player ids, result hash, replay id, settled time, battle server key id, Ed25519 signature field shape, and idempotent replay before reusing the existing server settlement path. Real signature verification, mTLS, protobuf decoding, and C++ service callbacks are still production work.
- Queue and room tickets can be cancelled while still waiting. Matched tickets cannot be cancelled through the queue API, and host room cancellation closes the waiting room.
- Heartbeat requests are read-only presence checks for authenticated sessions, waiting tickets, room lobbies, and active matches. They return server-authoritative queue, room, match, reconnect, loadout, tick, and event-cursor state without starting matches or implicitly reconnecting disconnected players.
- Room creation and room-code joins validate or resolve server-owned deck snapshots before a match can be created.
- Server creates match ids and server seeds.
- Input packets require monotonic tick and sequence values.
- Server advances the match timeline from accepted input ticks, emits authoritative bullet spawn/move/despawn deltas, validates card-slot requests against server hand/energy/cooldown state, tracks active cards, tracks Boss HP, and computes damage, graze, and hit counters.
- Bootstrap exposes a server-authoritative `world_boss` snapshot. World Boss entry consumes server-owned daily attempts when a match is created, settlement applies team damage once to global Boss HP, and defeat emits a single announcement marker. Instance Boss settlement requires server-side Boss HP to reach zero before a clear can be awarded.
- Match start, snapshots, settlements, and replay audit records carry the server-confirmed stage/character loadout so clients can present online play without trusting local selection state.
- Battle royale round-card selection and Boss card-transfer requests are validated by the server as mode actions. Accepted and rejected actions return server-owned mode state and emit match events.
- Server records authoritative match events with monotonically increasing cursors. HTTP clients can poll `/events` as a WSS stepping stone without changing match state.
- Disconnect marks the player disconnected, rejects further input while disconnected, and exposes a 30-second reconnect window. Reconnect restores the player connection and returns both `match_start` metadata and a full authoritative snapshot.
- Client-submitted `score`, `graze`, `hit`, `damage`, `position`, `reward_json`, `rank_score`, `boss_hp`, `active_cards`, card ids, energy, drops, client-authored upgrade results, and `client_authored_reward` fields are rejected.
- Large tick jumps are rejected so clients cannot force unbounded server fast-forward work.
- Match settlement and activity claim rewards are idempotent by `match_id:user_id` and `claim_kind:claim_id:user_id`.
- `match_end` responses include `server_authoritative=true`, replay metadata, rewards, task progress, event points, leaderboard updates, and mode result data.
- PvP Duel is exposed as a two-player mode with `pvp-duel-s0` ruleset metadata. Its current in-memory fallback uses the shared ready/input/snapshot/settlement path, but settlement deliberately leaves certification rank score and unlock state unchanged.
- Rematch requests are accepted only after the original match has fully settled and only from original participants. The server records each participant acceptance, emits rematch events on the original match, and creates a fresh loading match with the original mode, stage, deck snapshots, and loadouts once every participant accepts.
- Replay records are generated by the server at settlement time and can only be read by the participant that owns the returned `replay_id`.

## Pending

- Verified Nakama SDK tag build/plugin artifact for the SDK-neutral `runtime/nakamaapi` RPC/WSS-style dispatcher.
- Production business WSS streams on top of the current HTTP heartbeat, event-polling, allocation, and ticket fallback contracts.
- Repository wiring and persistent stores beyond the envelope audit sink adapter, including connecting `NewSQLBusinessEnvelopeAuditSink` to the production PostgreSQL handle.
- Full generated `PhK-Protocol` protobuf Go bindings for allocation/ticket/result messages instead of hand-shaped Go structs. The current dependency-light Go manifest is already consumed for version and field compatibility.
- C++ Battle Server real ticket verification, ECDHE/KCP/protobuf/ChaCha20 transport, production result signing, mTLS/protobuf battle result callbacks, and persistent battle result audits.
- Full deterministic STG simulation with bullet/card rules shared with SpellKard.
- Real-time room streaming, spectator streams, replay object storage, and production reconnect persistence beyond the current in-memory window.
- Production deployment, monitoring, and admin/liveops tooling.
