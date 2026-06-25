# Gensoulkyo Development Progress

Status date: 2026-06-24

| Area | Status | Notes |
| --- | --- | --- |
| Repository initialization | Done | README, licenses, docs, ignore rules, and directory plan created. |
| Runtime stack | Started | Nakama Go runtime module added with `InitModule`, config/profile/deck/inventory/chest/room/match RPCs, and `gensoulkyo.duel` match registration. |
| API contracts | Started | RPC, economy, deck, room-code lobby, match-create, and match op-code contracts documented in `docs/architecture.md`. |
| Database schema | Started | Initial PostgreSQL migration added from architecture docs, including open-alpha card and starter chest seed data. |
| Match simulation | Started | Deterministic input/movement state, state hash, snapshots, ready/start/end flow, participant join authorization, input validation/audit, settlement snapshots, and tests added. |
| Economy | Started | Persisted wallet/inventory reads and server-side chest pool/opening flow added for the open server. |
| Decks | Started | Deck save/list now uses PostgreSQL `player_decks`, validates enabled inventory ownership, enforces the open-alpha 20-card/2-copy rule, and passes active deck snapshots into match creation. |
| Rooms | Started | `match_rooms` schema and room create/join/start RPCs added for short-code 1v1 duel lobbies before full matchmaking queues. |
| Deployment | Started | Local Nakama/PostgreSQL Docker Compose files and runtime plugin builder added; runtime image build and compose healthcheck pass through the proxy. |

## Current Verification

Verified through the Nakama pluginbuilder container:

```sh
ALL_PROXY=socks5://10.10.10.108:10808 HTTPS_PROXY=socks5://10.10.10.108:10808 HTTP_PROXY=socks5://10.10.10.108:10808 https_proxy=socks5://10.10.10.108:10808 http_proxy=socks5://10.10.10.108:10808 all_proxy=socks5://10.10.10.108:10808 docker run --rm --entrypoint /bin/sh -e ALL_PROXY=socks5://10.10.10.108:10808 -e HTTPS_PROXY=socks5://10.10.10.108:10808 -e HTTP_PROXY=socks5://10.10.10.108:10808 -e https_proxy=socks5://10.10.10.108:10808 -e http_proxy=socks5://10.10.10.108:10808 -e all_proxy=socks5://10.10.10.108:10808 -e GOPROXY=https://goproxy.cn,direct -e GOSUMDB=off -v "$PWD:/src" -w /src heroiclabs/nakama-pluginbuilder:3.39.0 -c 'gofmt -w runtime/cards/catalog.go runtime/config/config.go runtime/match/input.go runtime/match/state.go runtime/match/handler.go runtime/match/persistence.go runtime/cards/deck_test.go runtime/match/state_test.go runtime/match/persistence_test.go && go test ./...'
ALL_PROXY=socks5://10.10.10.108:10808 HTTPS_PROXY=socks5://10.10.10.108:10808 HTTP_PROXY=socks5://10.10.10.108:10808 https_proxy=socks5://10.10.10.108:10808 http_proxy=socks5://10.10.10.108:10808 all_proxy=socks5://10.10.10.108:10808 docker build --build-arg ALL_PROXY=socks5://10.10.10.108:10808 --build-arg HTTPS_PROXY=socks5://10.10.10.108:10808 --build-arg HTTP_PROXY=socks5://10.10.10.108:10808 --build-arg https_proxy=socks5://10.10.10.108:10808 --build-arg http_proxy=socks5://10.10.10.108:10808 --build-arg all_proxy=socks5://10.10.10.108:10808 -f deployments/local/runtime-builder.Dockerfile -t gensoulkyo-runtime:code .
ALL_PROXY=socks5://10.10.10.108:10808 HTTPS_PROXY=socks5://10.10.10.108:10808 HTTP_PROXY=socks5://10.10.10.108:10808 https_proxy=socks5://10.10.10.108:10808 http_proxy=socks5://10.10.10.108:10808 all_proxy=socks5://10.10.10.108:10808 docker-compose -f deployments/local/docker-compose.yml up --build -d
curl -fsS http://127.0.0.1:7350/healthcheck
docker-compose -f deployments/local/docker-compose.yml logs --no-color --tail=180 nakama
docker-compose -f deployments/local/docker-compose.yml down --remove-orphans
git diff --check
```
