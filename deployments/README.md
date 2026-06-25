# deployments

Local self-hosting uses Nakama, PostgreSQL, and the Go runtime plugin.

## Local Docker Stack

From the repository root:

```sh
docker-compose -f deployments/local/docker-compose.yml up --build -d
curl -fsS http://127.0.0.1:7350/healthcheck
docker-compose -f deployments/local/docker-compose.yml down --remove-orphans
```

This execution server has legacy `docker-compose 1.29.2` available and does not have the Docker Compose v2 plugin. On machines with Compose v2, replace `docker-compose` with `docker compose`.

Expected endpoints:

- Nakama API/socket: `http://127.0.0.1:7350`
- Nakama console: `http://127.0.0.1:7351`

The example console credentials and server key in `configs/nakama.yml` are for local development only.

## Runtime Version Coupling

The Nakama image and `heroiclabs/nakama-pluginbuilder` image must stay on the same Nakama version. The current local default is `3.39.0`.

The runtime builder image compiles `gensoulkyo.so` into `/runtime-artifacts` and the one-shot `runtime-builder` service copies it into the shared Nakama module volume at startup.

## Database

The local PostgreSQL container applies SQL files from `migrations/` on first database initialization. For an existing volume, use a migration tool or recreate the `postgres-data` volume during local development.
