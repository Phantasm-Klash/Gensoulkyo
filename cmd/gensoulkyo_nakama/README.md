# gensoulkyo_nakama

Nakama Go Runtime binding for the Gensoulkyo business server.

This package is compiled only when the `nakama` build tag is enabled. The default local MVP stays a standard-library HTTP service, while this binding registers Nakama RPC entrypoints and forwards them into `runtime/nakamaapi`.

Planned build shape:

```powershell
go test -tags nakama ./cmd/gensoulkyo_nakama ./runtime/...
go build -tags nakama -buildmode=plugin -o gensoulkyo.so ./cmd/gensoulkyo_nakama
```

Local validation found `github.com/heroiclabs/nakama-common@v1.34.0` to be the newest tested SDK baseline here that still supports the repository's Go 1.20 Docker image. Newer Nakama common releases currently require Go 1.23+ or later, so pin or bump this dependency together with the Go/Docker baseline before enabling this tag build in CI.

The binding is intentionally thin: Nakama SDK context/session extraction and JSON payload wrapping happen here; security checks, audit snapshots, and business dispatch stay in `runtime/nakamaapi`.

When Nakama supplies a real `*sql.DB`, the module wires `security.NewSQLBusinessEnvelopeAuditSink`, `core.NewSQLBattleLifecycleAuditRepository`, and `core.NewSQLLobbyLifecycleAuditRepository`. The authenticated `battle.audit.status` and `lobby.audit.status` RPCs report whether lifecycle audit writes are durably configured and whether any repository write has failed.

`battle.result.submit` stays registered as a future migration path for service-to-service C++ Battle Server callbacks. This binding does not currently mark public Nakama RPC calls as service-origin, so `runtime/nakamaapi` rejects them before core result validation and players cannot use the RPC as an authority path for damage, rewards, or settlement.
