# gensoulkyo_nakama

Nakama Go Runtime binding for the Gensoulkyo business server.

This package is compiled only when the `nakama` build tag is enabled. The default local MVP stays a standard-library HTTP service, while this binding registers Nakama RPC entrypoints and forwards them into `runtime/nakamaapi`.

Planned build shape:

```powershell
go test -tags nakama ./cmd/gensoulkyo_nakama ./runtime/...
go build -tags nakama -buildmode=plugin -o gensoulkyo.so ./cmd/gensoulkyo_nakama
```

Local validation found `github.com/heroiclabs/nakama-common@v1.34.0` to be the newest tested SDK baseline here that still supports the repository's Go 1.20 Docker image. Newer Nakama common releases currently require Go 1.23+ or later, so pin or bump this dependency together with the Go/Docker baseline before enabling this tag build in CI.

For scoped validation without mutating the repository's `go.mod`/`go.sum`, run:

```sh
docker-compose --profile nakama-tag-build run --rm nakama-tag-build
```

If the default Go module proxy or checksum service is unreachable from the runner, use explicit disposable-container overrides:

```sh
docker-compose --profile nakama-tag-build run --rm -e GOPROXY=https://goproxy.cn,direct -e GOSUMDB=off nakama-tag-build
```

That profile copies the repository into a temporary container workspace, applies the local PhK-Protocol replace, temporarily pins `github.com/heroiclabs/nakama-common/runtime` to `NAKAMA_COMMON_VERSION` (default `v1.34.0`), runs the Nakama tag tests, and builds the Go Runtime plugin artifact at `/tmp/gensoulkyo.so`.

The binding is intentionally thin: Nakama SDK context/session extraction and JSON payload wrapping happen here; security checks, audit snapshots, and business dispatch stay in `runtime/nakamaapi`.

When Nakama supplies a real `*sql.DB`, the module wires `security.NewSQLBusinessEnvelopeAuditSink`, `core.NewSQLBattleLifecycleAuditRepository`, and `core.NewSQLLobbyLifecycleAuditRepository`. The authenticated `business.envelope.audit.status` RPC reports the shared envelope guard snapshot, including durable sink write errors; `battle.audit.status` and `lobby.audit.status` report whether lifecycle audit repositories are configured and whether any repository write has failed.

`battle.servers.register`, `battle.servers.heartbeat`, and `battle.servers.offline` are registered for service-to-service battle server lifecycle callbacks. The binding marks them as service-origin only for the explicit callback allowlist, only when Nakama supplies no player `session_id`/`user_id`, and only when `runtime.RUNTIME_CTX_MODE` is present and not `client`. Player-scoped calls therefore fail closed in `runtime/nakamaapi`; authenticated clients should only use `battle.servers` for business-envelope-protected discovery. The WSS dispatcher also rejects these service-origin-only names before envelope validation, so a client socket attempt cannot consume replay-guard seq/nonce state.

`battle.result.submit` is registered for service-to-service C++ Battle Server callbacks under the same allowlist and context gate. Public player calls still fail before core result validation, and the name is not accepted on WSS, so clients cannot use RPC or socket traffic as an authority path for damage, rewards, or settlement.
