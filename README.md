# Gensoulkyo

Gensoulkyo is the open-source server project for Phantasm Klash.

It provides the planned authoritative online layer for accounts, sessions, lobbies, match rooms, deterministic match state, replay metadata, player inventory, decks, rewards, events, and leaderboards.

## Current State

This repository now contains the first open-source Nakama Go runtime scaffold, initial PostgreSQL schema, and local Docker deployment files. The first runtime milestones cover config/profile/deck/inventory/chest/room/match RPCs, PostgreSQL-backed deck persistence, active-deck validation, room-code duel lobbies, an authoritative match shell, deterministic input application, snapshots, match persistence, idempotent local reward settlement, and local chest-opening primitives.

## Planned Responsibilities

- Non-platform-specific account and session services.
- Lobby, room code, and matchmaking flows.
- Server-authoritative match simulation.
- Input ingestion and validated card requests.
- Match snapshots, replay metadata, and audit records.
- PostgreSQL migrations and persistence.
- Self-hosted deployment documentation.
- Open-source reward and inventory primitives.

## Directory Plan

- `cmd/`: service entrypoints.
- `runtime/`: authoritative gameplay and service runtime modules.
- `migrations/`: database migrations.
- `deployments/`: self-hosting and local deployment files.
- `configs/`: example configuration only.
- `tests/`: unit, integration, and replay determinism tests.
- `docs/`: server implementation notes.
- `dev/`: server development progress notes.

## Local Development

Run tests when Go is available:

```sh
go test ./...
```

On this server, host Go is not installed; use the Nakama pluginbuilder container:

```sh
ALL_PROXY=socks5://10.10.10.108:10808 HTTPS_PROXY=socks5://10.10.10.108:10808 HTTP_PROXY=socks5://10.10.10.108:10808 docker run --rm --entrypoint /bin/sh -e ALL_PROXY=socks5://10.10.10.108:10808 -e HTTPS_PROXY=socks5://10.10.10.108:10808 -e HTTP_PROXY=socks5://10.10.10.108:10808 -e https_proxy=socks5://10.10.10.108:10808 -e http_proxy=socks5://10.10.10.108:10808 -e all_proxy=socks5://10.10.10.108:10808 -e GOPROXY=https://goproxy.cn,direct -e GOSUMDB=off -v "$PWD:/src" -w /src heroiclabs/nakama-pluginbuilder:3.39.0 -c 'go test ./...'
```

Build and run the local Nakama/PostgreSQL stack:

```sh
docker-compose -f deployments/local/docker-compose.yml up --build -d
curl -fsS http://127.0.0.1:7350/healthcheck
docker-compose -f deployments/local/docker-compose.yml down --remove-orphans
```

The local stack uses example credentials only. Do not reuse them for public deployments.

## Boundary

This repository must not include commercial platform SDK files, private API keys, closed economy parameters, anti-fraud operational secrets, or unlicensed media.

## Licensing

Code is licensed under MIT. Documentation and original non-code text are licensed under CC BY 4.0 unless a file states otherwise.
