# gensoulkyo_nakama

Nakama Go Runtime binding for the Gensoulkyo business server.

This package is compiled only when the `nakama` build tag is enabled. The default local MVP stays a standard-library HTTP service, while this binding registers Nakama RPC entrypoints and forwards them into `runtime/nakamaapi`.

Planned build shape:

```powershell
go test -tags nakama ./cmd/gensoulkyo_nakama ./runtime/...
go build -tags nakama -buildmode=plugin -o gensoulkyo.so ./cmd/gensoulkyo_nakama
```

The binding is intentionally thin: Nakama SDK context/session extraction and JSON payload wrapping happen here; security checks, audit snapshots, and business dispatch stay in `runtime/nakamaapi`.

When Nakama supplies a real `*sql.DB`, the module wires `security.NewSQLBusinessEnvelopeAuditSink`, `core.NewSQLBattleLifecycleAuditRepository`, and `core.NewSQLLobbyLifecycleAuditRepository`. The authenticated `battle.audit.status` and `lobby.audit.status` RPCs report whether lifecycle audit writes are durably configured and whether any repository write has failed.
