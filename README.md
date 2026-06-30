# Gensoulkyo

Gensoulkyo is the open-source server project for Phantasm Klash.

It provides the planned authoritative online layer for accounts, sessions, lobbies, match rooms, deterministic match state, replay metadata, player inventory, decks, card upgrades, chest pools/openings, rewards, events, and leaderboards.

## Current State

This repository now contains the first open-source server MVP:

- a Go runtime core for anonymous sessions, server-owned in-memory inventory/decks/card upgrades/chests, authoritative chest pool opening, matchmaking, ticket cancellation, room-code lobby flow, low-frequency lobby messages, server-validated deck/stage/character loadouts, session/ticket/match heartbeat presence, ready flow, battle server registration/heartbeat/offline lifecycle, battle allocation, Ed25519-signed battle tickets, battle-ticket consumption audit, tick input ingestion, validated card requests, server-validated mode actions, deterministic stage-aware server-side bullet simulation, active-card snapshots, world-boss state snapshots and settlement HP deduction, instance-boss clear/fail adjudication, cursor-based match event polling, 30-second reconnect restore, server-owned match settlement, post-settlement rematch confirmation, server-generated replay audit records, rewards, tasks, events, leaderboards, and activity claims;
- a small HTTP adapter under `/v1/...` that mirrors the future Nakama RPC, WSS business notification, and battle-allocation contracts;
- a SDK-neutral Nakama adapter plus a `nakama` build-tagged Go Runtime binding source under `cmd/gensoulkyo_nakama`, so real Nakama RPC registration can stay as a thin outer layer over the tested runtime dispatcher;
- unit and HTTP integration tests for the server-authority boundary;
- an optional standard-library database configuration path, `.up.sql` migration runner, business-envelope audit sink, and battle/lobby lifecycle audit repository wiring for future PostgreSQL-backed Nakama deployment.

The MVP is intentionally in-memory by default. PostgreSQL repository persistence, production deployment, real business WSS streaming, C++ Battle Server KCP/protobuf transport, production X25519/AEAD/sign envelope handling, and full shared client/server bullet-card rules are still pending. The current binary registers the open-source pgx `database/sql` driver and can run migrations when a database URL is explicitly provided.

## Planned Responsibilities

- Non-platform-specific account and session services.
- Lobby, room code, and matchmaking flows.
- Battle server discovery, lifecycle status, allocation, signed battle ticket issuance, and ticket consumption audit.
- Server-authoritative match simulation.
- Input ingestion and validated card requests.
- Match snapshots, replay metadata, and audit records.
- PostgreSQL migrations and persistence.
- Self-hosted deployment documentation.
- Open-source reward and inventory primitives.

## Directory Plan

- `cmd/`: service entrypoints. `cmd/gensoulkyo` starts the local HTTP service.
- `cmd/gensoulkyo_nakama/`: optional `nakama` build-tagged Nakama Go Runtime binding source.
- `runtime/`: authoritative gameplay and service runtime modules. `runtime/core` owns server state and validation; `runtime/httpapi` exposes it over HTTP; `runtime/nakamaapi` dispatches Nakama-style RPC/WSS business calls; `runtime/security` owns envelope guards/audit sinks; `runtime/storage` owns optional database opening and migration helpers.
- `migrations/`: database migrations.
- `deployments/`: self-hosting and local deployment files.
- `configs/`: example configuration only.
- `tests/`: unit, integration, and replay determinism tests.
- `docs/`: server implementation notes.
- `dev/`: server development progress notes.

## Local Run

```powershell
go test ./...
go run ./cmd/gensoulkyo -addr 127.0.0.1:7350
```

Optional migration flags, for builds that register a real `database/sql` driver:

```powershell
go run ./cmd/gensoulkyo -database-url postgres://phantasm:phantasm@localhost:5432/gensoulkyo?sslmode=disable -migrate-up
```

`-database-driver` defaults to `pgx` when a database URL is present.

Optional Nakama runtime binding source check:

```powershell
go test ./cmd/gensoulkyo_nakama
```

When `github.com/heroiclabs/nakama-common/runtime` is available in the build environment, the intended plugin check is:

```powershell
go test -tags nakama ./cmd/gensoulkyo_nakama
go build -tags nakama -buildmode=plugin -o gensoulkyo.so ./cmd/gensoulkyo_nakama
```

Useful endpoints:

- `GET /health`
- `GET /v1/security/business-envelope`
- `GET /v1/security/battle-audit`
- `GET /v1/security/lobby-audit`
- `POST /v1/auth/anonymous`
- `GET /v1/bootstrap`
- `GET /v1/inventory`
- `POST /v1/cards/upgrade`
- `GET /v1/decks`
- `POST /v1/decks/save`
- `GET /v1/chests`
- `POST /v1/chests/open`
- `POST /v1/presence/heartbeat`
- `POST /v1/matchmaking/join`
- `GET /v1/matchmaking/tickets/{ticket_id}`
- `POST /v1/matchmaking/tickets/{ticket_id}/cancel`
- `POST /v1/rooms/create`
- `GET /v1/rooms`
- `GET /v1/rooms/{room_code}`
- `GET /v1/rooms/{room_code}/rules`
- `POST /v1/rooms/{room_code}/join`
- `POST /v1/rooms/{room_code}/leave`
- `POST /v1/rooms/{room_code}/messages`
- `GET /v1/battle/servers`
- `POST /v1/battle/servers/register`
- `POST /v1/battle/servers/heartbeat`
- `POST /v1/battle/servers/offline`
- `POST /v1/battle/tickets/consume`
- `POST /v1/battle/results/submit`
- `POST /v1/matches/{match_id}/ready`
- `GET /v1/matches/{match_id}/battle-allocation`
- `POST /v1/matches/{match_id}/battle-ticket`
- `POST /v1/matches/{match_id}/input`
- `GET /v1/matches/{match_id}/snapshot`
- `GET /v1/matches/{match_id}/events?after={cursor}&limit={n}`
- `POST /v1/matches/{match_id}/mode-action`
- `POST /v1/matches/{match_id}/disconnect`
- `POST /v1/matches/{match_id}/reconnect`
- `POST /v1/matches/{match_id}/settle`
- `POST /v1/matches/{match_id}/rematch`
- `GET /v1/replays/{replay_id}`
- `POST /v1/activity/claim`

Authenticated endpoints accept `Authorization: Bearer <session_token>` or `X-Session-Token`.

## Boundary

This repository must not include commercial platform SDK files, private API keys, closed economy parameters, anti-fraud operational secrets, or unlicensed media.

Clients may submit only intent: login metadata, deck-save requests, card-upgrade requests, chest-open requests, queue or room requests, low-frequency lobby chat/announcement text, queue-ticket cancellation before a match is created, heartbeat presence hints, stage/character preferences in `mode_params`, ready, tick input, card-slot requests, mode-action requests, event-poll cursors, disconnect/reconnect requests, post-settlement rematch acceptance, replay reads for their own server-generated records, and activity claim requests. The server validates deck saves against its owned inventory, resolves active deck snapshots before matchmaking, validates card ownership/level/cost for upgrades, validates chest pool ownership/cost/pity/result generation, validates loadouts, buckets open matchmaking by mode and stage, keeps room-code matches on the host stage, stores lobby messages with idempotent message ids and host-only announcements, creates battle allocations, signs short-lived battle tickets, records service-origin ticket consumption, creates rematch matches only after all original participants accept, and echoes server-authoritative loadout/allocation data in queue, heartbeat, match-start, snapshot, settlement, rematch, and replay responses. Score, graze, hits, rewards, rank, Boss HP, active-card state, server mode state, event contents, card ids, energy, drops, replay contents, chest results, card upgrade results, battle result fields, battle ticket signatures, and lobby metadata authority fields remain server-owned and are rejected when submitted by clients.

## Licensing

Code is licensed under MIT. Documentation and original non-code text are licensed under CC BY 4.0 unless a file states otherwise.
