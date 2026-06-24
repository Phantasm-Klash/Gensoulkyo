# Gensoulkyo

Gensoulkyo is the open-source server project for Phantasm Klash.

It provides the planned authoritative online layer for accounts, sessions, lobbies, match rooms, deterministic match state, replay metadata, player inventory, decks, rewards, events, and leaderboards.

## Current State

This repository is initialized as the server workspace. The directory plan is present, while implementation remains empty until the first service contracts are approved in the `docs` repository.

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

## Boundary

This repository must not include commercial platform SDK files, private API keys, closed economy parameters, anti-fraud operational secrets, or unlicensed media.

## Licensing

Code is licensed under MIT. Documentation and original non-code text are licensed under CC BY 4.0 unless a file states otherwise.

