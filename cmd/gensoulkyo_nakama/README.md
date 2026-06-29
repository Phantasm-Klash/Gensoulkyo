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

That profile copies the repository into a temporary container workspace, applies the local PhK-Protocol replace, temporarily pins `github.com/heroiclabs/nakama-common/runtime` to `NAKAMA_COMMON_VERSION` (default `v1.34.0`), runs the Nakama tag tests, and builds the Go Runtime plugin artifact at `/tmp/gensoulkyo.so`.

The binding is intentionally thin: Nakama SDK context/session extraction and JSON payload wrapping happen here; security checks, audit snapshots, and business dispatch stay in `runtime/nakamaapi`.

When Nakama supplies a real `*sql.DB`, the module wires `security.NewSQLBusinessEnvelopeAuditSink`, `core.NewSQLBattleLifecycleAuditRepository`, and `core.NewSQLLobbyLifecycleAuditRepository`. The authenticated `business.envelope.audit.status` RPC reports the shared envelope guard snapshot, including durable sink write errors; `battle.audit.status` and `lobby.audit.status` report whether lifecycle audit repositories are configured and whether any repository write has failed.

`battle.servers.register` and `battle.servers.heartbeat` are registered for service-to-service battle server lifecycle callbacks, but the public binding does not currently set service-origin metadata. Player-scoped calls therefore fail closed in `runtime/nakamaapi`; authenticated clients should only use `battle.servers` for business-envelope-protected discovery.

`battle.result.submit` stays registered as a future migration path for service-to-service C++ Battle Server callbacks. This binding does not currently mark public Nakama RPC calls as service-origin, so `runtime/nakamaapi` rejects them before core result validation and players cannot use the RPC as an authority path for damage, rewards, or settlement.
