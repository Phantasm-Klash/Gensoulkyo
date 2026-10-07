# gensoulkyo_nakama

Nakama Go Runtime binding for the Gensoulkyo business server.

This package is compiled only when the `nakama` build tag is enabled. The default local MVP stays a standard-library HTTP service, while this binding registers Nakama RPC entrypoints and forwards them into `runtime/nakamaapi`.

## Build shape

`go.mod` durably pins `github.com/heroiclabs/nakama-common v1.48.0` and declares
`go 1.27.1`, matching the `heroiclabs/nakama:3.41.0` server line, so the tag
build runs out of the box:

```sh
go test -tags nakama ./cmd/gensoulkyo_nakama ./runtime/...
go build -tags nakama -buildmode=plugin -o gensoulkyo.so ./cmd/gensoulkyo_nakama
```

Go plugins only load when the plugin and the Nakama server were built with the
exact same Go toolchain, so the deployable artifact must be produced inside
`heroiclabs/nakama-pluginbuilder:<version>` rather than a local Go install. Use
`deployments/nakama/build-plugin.sh`, or the compose profile:

```sh
docker-compose --profile nakama-tag-build run --rm nakama-tag-build
```

That profile runs the pluginbuilder matching the pinned Nakama version, applies
the local PhK-Protocol replace, builds the Go Runtime plugin artifact, and never
mutates the repository's `go.mod`/`go.sum`.

If the default Go module proxy or checksum service is unreachable from the
runner, the profile already defaults to `GOPROXY=https://goproxy.cn,direct` and
`GOSUMDB=off`; override them with `GOPROXY=... GOSUMDB=... docker-compose ...`.

See `deployments/nakama/` for the full self-hosted Nakama + PostgreSQL stack
(plugin build, migrations, server and web console).

The binding is intentionally thin: Nakama SDK context/session extraction and JSON payload wrapping happen here; security checks, audit snapshots, and business dispatch stay in `runtime/nakamaapi`.

When Nakama supplies a real `*sql.DB`, the module wires `security.NewSQLBusinessEnvelopeAuditSink`, `core.NewSQLBattleLifecycleAuditRepository`, and `core.NewSQLLobbyLifecycleAuditRepository`. The authenticated `business.envelope.audit.status` RPC reports the shared envelope guard snapshot, including durable sink write errors; `battle.audit.status` and `lobby.audit.status` report whether lifecycle audit repositories are configured and whether any repository write has failed.

`business.contract` is registered as an authenticated, business-envelope-protected RPC/WSS-style read that returns the shared low-frequency business transport, notification topic, service callback context/accepted flag values, non-player/non-envelope callback booleans, authority, and forbidden-field contract without requiring a room. `business.event` is registered as a player-scoped, business-envelope-protected RPC/WSS-style contract for low-frequency Nakama status/socket payloads: queue progress, room snapshots, matchmaking found, ready state, battle allocation, and signed battle ticket delivery. `business.event.settlement` is also registered as a settlement-only alias for server-authored settlement projections; it binds the request kind to `settlement` and rejects conflicting kinds or client-authored result fields. Business event request and notification contracts expose `client_request_authority=lookup_only`, so clients can only request server projections by `kind`, `ticket_id`, `room_code`, or `match_id`. These RPC/WSS contracts are not battle tick channels, and they do not authorize client result submission; C++ battle result callbacks remain service-origin-only.

`match.rematch` / `matches.rematch` is registered for authenticated RPC and WSS-style client calls as a low-frequency post-settlement intent. It requires the business envelope, only accepts original match participants, only succeeds after all participant settlements exist, and creates the next loading match only after every original participant accepts. It does not expose `match.settle`, `battle.result.submit`, or any high-frequency battle tick path on Nakama WSS.

`battle.servers.register`, `battle.servers.heartbeat`, and `battle.servers.offline` are registered for service-to-service battle server lifecycle callbacks. The binding marks them as service-origin only for the explicit callback allowlist, only when Nakama supplies no player `session_id`/`user_id`, only when `runtime.RUNTIME_CTX_MODE` is `rpc`, and only when `runtime.RUNTIME_CTX_VARS` includes `gensoulkyo_service_origin=battle_server` plus `gensoulkyo_battle_callback=true` (or `1`/`yes`). Player-scoped calls and unmarked server/runtime calls therefore fail closed in `runtime/nakamaapi`; authenticated clients should only use `battle.servers` for business-envelope-protected discovery. The WSS dispatcher also rejects these service-origin-only names before envelope validation, so a client socket attempt cannot consume replay-guard seq/nonce state.

`battle.agent.assignments` is registered for service-to-service battle agent polling: it returns the live match allocations currently routed to a given `battle_server_id` (match id, mode, ruleset, endpoint, server seed, mode config hash, and player roster) so an out-of-process agent knows which matches it must spawn a battle process for. Settled matches drop out automatically, letting the agent reap the corresponding process. It is restricted to the service callback allowlist and is not exposed to clients.

Out-of-process battle servers (for example an agent running on a separate host from Nakama) cannot satisfy the in-process service-origin gate because Nakama only fills `runtime.RUNTIME_CTX_VARS` for calls that originate inside a runtime. The binding therefore also accepts the shared callback secret set in `GENSOULKYO_SERVICE_CALLBACK_KEY` (falling back to `NAKAMA_RUNTIME_HTTP_KEY`), presented either as the `X-Gensoulkyo-Service-Key` HTTP header or the `service_key` query parameter. Nakama exposes both to HTTP RPC handlers via `runtime.RUNTIME_CTX_HEADERS` / `runtime.RUNTIME_CTX_QUERY_PARAMS`. The secret never widens the operation allow-list (only `core.ServiceCallbackOperations()` are eligible) and requests carrying a player `session_id` or `user_id` are always rejected, so a leaked secret cannot be replayed as a player.

`battle.ticket.consume` and `battle.result.submit` are registered for service-to-service C++ Battle Server callbacks under the same allowlist and context gate. Ticket consumption must echo the ticket's protocol, business API, battle API, ruleset version stamp, and signed `mode_config_hash` before an issued short-lived battle ticket is marked as accepted by the battle endpoint and the existing `consumed` ticket audit transition is written; repeated consumes are idempotent. Battle result submission must carry the same full version stamp and match the allocated match/ruleset before settlement is accepted. Public player calls still fail before core ticket/result validation, and these names are not accepted on WSS, so clients cannot use RPC or socket traffic as an authority path for ticket use, damage, rewards, or settlement.
