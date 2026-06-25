# tests

The first server tests live next to the Go runtime packages.

Run from the repository root when Go is installed:

```sh
go test ./...
```

Equivalent containerized test command used on this server:

```sh
ALL_PROXY=socks5://10.10.10.108:10808 HTTPS_PROXY=socks5://10.10.10.108:10808 HTTP_PROXY=socks5://10.10.10.108:10808 https_proxy=socks5://10.10.10.108:10808 http_proxy=socks5://10.10.10.108:10808 all_proxy=socks5://10.10.10.108:10808 docker run --rm --entrypoint /bin/sh -e ALL_PROXY=socks5://10.10.10.108:10808 -e HTTPS_PROXY=socks5://10.10.10.108:10808 -e HTTP_PROXY=socks5://10.10.10.108:10808 -e https_proxy=socks5://10.10.10.108:10808 -e http_proxy=socks5://10.10.10.108:10808 -e all_proxy=socks5://10.10.10.108:10808 -e GOPROXY=https://goproxy.cn,direct -e GOSUMDB=off -v "$PWD:/src" -w /src heroiclabs/nakama-pluginbuilder:3.39.0 -c 'go test ./...'
```

Docker-based runtime build:

```sh
ALL_PROXY=socks5://10.10.10.108:10808 HTTPS_PROXY=socks5://10.10.10.108:10808 HTTP_PROXY=socks5://10.10.10.108:10808 docker build --build-arg ALL_PROXY=socks5://10.10.10.108:10808 --build-arg HTTPS_PROXY=socks5://10.10.10.108:10808 --build-arg HTTP_PROXY=socks5://10.10.10.108:10808 -f deployments/local/runtime-builder.Dockerfile -t gensoulkyo-runtime .
```

Local integration smoke test:

```sh
docker-compose -f deployments/local/docker-compose.yml up --build -d
curl -fsS http://127.0.0.1:7350/healthcheck
docker-compose -f deployments/local/docker-compose.yml logs --no-color --tail=120 nakama
docker-compose -f deployments/local/docker-compose.yml down --remove-orphans
```

Room/deck/match integration coverage still needs a live Nakama client harness. The manual smoke target is: authenticate two users, save one active 20-card deck per user, call `gensoulkyo.room.create`, `gensoulkyo.room.join`, `gensoulkyo.room.start`, connect both users to the returned `nakama_match_id`, send ready op `1`, and verify `matches`, `match_players`, and `match_rooms` rows.
