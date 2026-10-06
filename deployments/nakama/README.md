# Gensoulkyo on Nakama (self-hosted)

This directory runs the real [Nakama](https://heroiclabs.com/nakama/) server
with the Gensoulkyo business logic loaded as a Go runtime plugin, plus the
PostgreSQL database Nakama requires.

Nakama owns the standard platform features — accounts, sessions, the storage
engine, leaderboards, parties/groups, friends, chat, matchmaking and the web
console. Gensoulkyo contributes the Phantasm Klash business layer through
`cmd/gensoulkyo_nakama`, which registers the `*.` RPCs (auth, inventory, decks,
chests, matchmaking, rooms, battle allocation/tickets, results) and forwards
them into `runtime/nakamaapi`.

## Layout

| File | Purpose |
| --- | --- |
| `docker-compose.yml` | postgres + one-shot migrations + nakama |
| `local.yml` | Nakama server config (mounted at `/nakama/data/local.yml`) |
| `Dockerfile` | Optional: self-contained image with the plugin baked in |
| `build-plugin.sh` | Builds `modules/gensoulkyo.so` with the matching pluginbuilder |
| `modules/` | Plugin output directory (git-ignored, mounted into the container) |
| `.env.example` | Copy to `.env` to override versions/ports/secrets |

## Quick start

```sh
cd Gensoulkyo/deployments/nakama
cp .env.example .env          # optional
./build-plugin.sh             # -> modules/gensoulkyo.so
docker compose up -d
docker compose logs -f nakama
```

Endpoints once healthy:

- `http://<host>:7350` — HTTP/WebSocket API (clients use this)
- `http://<host>:7351` — web console (default login `admin` / `password`)
- `<host>:7349` — gRPC API

## Why the plugin is built in Docker

Go plugins (`-buildmode=plugin`) only load when the plugin and the host binary
were produced by the **exact same Go version**. `build-plugin.sh` therefore
builds inside `heroiclabs/nakama-pluginbuilder:<NAKAMA_VERSION>`, which pins the
same toolchain as `heroiclabs/nakama:<NAKAMA_VERSION>`. Building the plugin with
an arbitrary local Go install will fail at load time with a version mismatch.

Version pairing is enforced by `go.mod`:

| Nakama | pluginbuilder Go | `nakama-common` |
| --- | --- | --- |
| 3.41.0 | 1.27.1 | v1.48.0 |

`Gensoulkyo/go.mod` requires `github.com/heroiclabs/nakama-common v1.48.0` and
declares `go 1.27.1`, so `go test -tags nakama ./...` works out of the box.

## Database migrations

Two independent migration sets run against the same database:

1. `nakama migrate up` — Nakama's own schema (users, storage, leaderboards, …),
   executed by the `nakama` service entrypoint.
2. `Gensoulkyo/migrations/*.up.sql` — the Gensoulkyo audit schema
   (`business_envelope_audits`, `battle_*`, `lobby_*`, …), applied by the
   one-shot `migrate` service.

Both are idempotent. To apply the Gensoulkyo migrations from a binary instead
of `psql`, use the standalone server:

```sh
go run ./cmd/gensoulkyo \
  -database-url 'postgres://postgres:localdb@127.0.0.1:5432/nakama?sslmode=disable' \
  -migrate-up
```

## Verifying the deployment

```sh
# 1. server health
curl -s http://127.0.0.1:7350/healthcheck          # -> {}

# 2. authenticate (device auth returns a session token)
TOKEN=$(curl -s -X POST http://127.0.0.1:7350/v2/account/authenticate/device \
  -H 'Content-Type: application/json' \
  -H 'Authorization: Basic ZGVmYXVsdGtleTo=' \
  -d '{"id":"dev-verify-0001","create":true}' \
  | sed -n 's/.*"token":"\([^"]*\)".*/\1/p')

# 3. call a Gensoulkyo RPC through Nakama
NOW=$(date +%s%3N)
PAYLOAD=$(printf '"{\\"business_envelope\\":{\\"version\\":\\"business-v0-scaffold\\",\\"seq\\":1,\\"timestamp_ms\\":%s,\\"nonce\\":\\"verify-nonce-0001\\",\\"op\\":\\"bootstrap\\",\\"key_id\\":\\"client-dev-key\\",\\"auth_tag\\":\\"%064d\\",\\"ciphertext_mode\\":\\"aead\\",\\"body_hash\\":\\"\\"}}" 0' "$NOW")
curl -s -X POST http://127.0.0.1:7350/v2/rpc/bootstrap \
  -H 'Content-Type: application/json' \
  -H "Authorization: Bearer $TOKEN" \
  --data-raw "$PAYLOAD"
```

Two details matter here:

- Nakama's HTTP RPC payload is a **JSON string** (not a raw object), so the body is
  double-encoded.
- Authenticated business RPCs require a **business envelope** whose `key_id` is
  registered in `business_envelope_keys`. The dev seed provides `client-dev-key`
  for Nakama RPC/WSS and `dev-business-envelope-v0` for the HTTP fallback. An
  unknown `key_id` still runs the business call but fails the audit insert on the
  `fk_business_envelope_audit_key` foreign key, which shows up as `audit_errors`
  in the `business.envelope.audit.status` RPC.

A successful call returns
`{"payload":"{\"ok\":true,...\"session_token\":\"nakama-http-session:...\"}"}`.
Accepted and rejected envelopes are persisted to `business_envelope_audits`, so
you can confirm the durable audit path with:

```sh
docker compose exec postgres \
  psql -U postgres -d nakama \
  -c "select accepted, endpoint, error_code, key_id from business_envelope_audits order by audit_id desc limit 5"
```

Nakama's HTTP RPC context forwards the authenticated `user_id` but no session id
(`server/api_rpc.go` passes an empty session id). The binding derives a stable
core session token (`nakama-http-session:<user_id>`) from the user id so HTTP
calls reach the same session model as the WebSocket path.

`docker compose logs nakama` prints
`Gensoulkyo Nakama runtime registered N RPC handlers` when the plugin loads.

## Production notes

- Put TLS in front of 7350/7351 (Nakama does not terminate TLS itself).
- Change `socket.server_key` and `runtime.http_key` in `local.yml` (or via
  `NAKAMA_*` env vars) before exposing the server publicly.
- The Gensoulkyo core is still in-memory by default; the PostgreSQL wiring in
  this stack currently persists the audit streams, not the full business state.
- Back up the `nakama_pgdata` volume; economy and audit rows are the
  high-priority recovery data.
