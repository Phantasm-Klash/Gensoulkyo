# Gensoulkyo Development Progress

Status date: 2026-06-28

| Area | Status | Notes |
| --- | --- | --- |
| Repository initialization | Done | README, licenses, docs, ignore rules, and directory plan created. |
| Runtime stack | Started | Go module added with a standard-library HTTP MVP. Domain logic is isolated so it can later move behind Nakama Go Runtime, PostgreSQL, and service-to-service battle callbacks. Transport-neutral migration security helpers now live in `runtime/security` so HTTP, future Nakama RPC, and business WSS can reuse the same business envelope request adapter, replay guard, audit sink interface, and standard-library `database/sql` audit writer. The adapter now has SDK-neutral constructors for HTTP headers, Nakama RPC-style payloads, and Nakama WSS-style payloads. `runtime/nakamaapi` now maps external Nakama user/session ids into core sessions and dispatches key business RPC/WSS-style calls through the shared envelope guard; the Nakama lobby MVP now exposes room create/list/get/rules/join/leave/message, match ready, heartbeat, battle allocation/ticket reads, and owner-scoped replay reads on RPC/business WSS, while keeping battle result submission behind an explicit service-origin RPC gate and rejecting client-origin RPC/WSS result submission. `runtime/core` now emits battle lifecycle audit records for allocation, battle ticket issuance, battle result verification, and per-player replay settlement through an injected repository, and includes a `database/sql` writer for allocation, ticket, result, and replay audit rows. The PostgreSQL migration now matches those repository inserts, persists server-authoritative markers for allocation/ticket audits, and seeds the development Ed25519 ticket-signing key id used by signed ticket audits. `cmd/gensoulkyo_nakama` contains a `nakama` build-tagged SDK binding source, wires the Nakama `*sql.DB` into the envelope audit sink plus battle/lobby lifecycle audit repositories, keeps client/player RPC calls fail-closed, and now marks only allowlisted battle server lifecycle/result callback RPCs as service-origin when Nakama provides no player context, `runtime.RUNTIME_CTX_MODE` is `rpc`, and `runtime.RUNTIME_CTX_VARS` carries the canonical battle-server callback origin and flag from `core.ServiceCallbackContext()`; source-shape tests pin SQL audit wiring, battle allocation/ticket/result/replay registration, and the callback context gate. Current default tests keep Nakama SDK optional; real `go test -tags nakama ./cmd/gensoulkyo_nakama` is validated through the disposable Docker Compose `nakama-tag-build` profile until a durable `github.com/heroiclabs/nakama-common/runtime` module pin can be added with the Go/Docker baseline decision. `runtime/storage` now provides optional database opening and `.up.sql` migration helpers without forcing persistence on the local MVP. |
| API contracts | Started | Anonymous login, bootstrap with inventory/decks/chests/world-boss snapshot, inventory read, card upgrade, deck list read, deck save, chest list/open, heartbeat presence, matchmaking join/ticket/cancel, room create/list/get/rules/join/leave/message, battle server register/list/heartbeat, battle allocation/ticket read, battle result submit, match ready/input/snapshot/events/mode-action/disconnect/reconnect/settle/rematch, replay read, activity claim, and development business-envelope status HTTP fallback contracts implemented. Bootstrap now includes `pvp_duel` as a 2-player mode with `pvp-duel-s0` ruleset/reward metadata, and queue/ready/input/settlement paths preserve that mode contract. Room rules snapshots expose protocol/ruleset/mode versions, mode config hash, tick rate, input delay, battle ticket TTL, allowed client intents, server authority fields, and forbidden client-authored fields. Lobby rooms now treat duplicate create/join/leave as idempotent where a stable ticket exists, transfer host authority to the next waiting participant when the host leaves, and remove empty waiting rooms from the active room index. Lobby messages support chat and host-only announcement metadata with server timestamps, participant permissions, message-id idempotency, and the same recursive forbidden authority-field guard. Authenticated HTTP fallback routes now also accept optional `X-PhK-Business-*` scaffold envelope headers and reject invalid/replayed seq, nonce, and stale/future timestamps when those headers are present. The Nakama-style adapter skeleton requires business envelopes for authenticated RPC/WSS-style calls and records missing/invalid/replayed envelope audits through the same guard, exposes lobby, ready, battle ticket, and owner-authorized replay reads to clients, and requires service-origin metadata before `battle.result.submit` can reach core signed-result validation. |
| Shared protocol integration | Started | `D:\gotouhou\PhK-Protocol` exists with first protobuf/ruleset/schema draft, descriptor export, and dependency-light Go manifest export. Gensoulkyo now consumes `github.com/phantasm-klash/phk-protocol/gen/go/phk/v1` through a local replace, derives its protocol/business/battle/ruleset version constants from that manifest, and has contract tests for required envelope/ticket/allocation/result fields. Full protobuf Go bindings, real Nakama SDK RPC/WSS tag-build verification, real X25519/AEAD/sign envelope handling, PostgreSQL persistence, mTLS service callbacks, and real C++ Battle Server result signature verification are pending. |
| Database schema | Started | PostgreSQL migration draft `001_business_security_audit` defines business envelope/signing key registry, envelope audit log, nonce-window checkpoint table, battle ticket audit table, match allocation audit table, battle result audit table, replay audit table, lobby room audit table, and lobby message audit table. `runtime/security` now includes a `database/sql` writer for envelope audit rows, `runtime/core` includes `database/sql` lifecycle audit repositories for allocation/ticket/result/replay rows and lobby room/message rows, `runtime/storage` can apply `.up.sql` migrations, and `cmd/gensoulkyo` registers pgx stdlib as the default driver when a database URL is supplied. Broader PostgreSQL persistence for users/sessions/inventory/decks/chests/rewards/leaderboards/world Boss state is still pending. |
| Match simulation | Started | In-memory authoritative match state owns default inventory and saved decks, validates deck saves against owned cards/copy/rarity/interference/ranked/ruleset constraints, resolves active server deck snapshots for queue and room-code matchmaking, validates stage/character/rating loadouts, buckets certification matchmaking by rating/stage, supports `pvp_duel` 1v1 queue/start/input/mode-state/settlement contract, supports pre-match queue/room ticket cancellation and explicit room leave semantics with host transfer and empty-room cleanup, lists waiting rooms, returns room snapshots and room rules snapshots with deck hashes only, allocates a battle server with same-load preference for mode-dedicated servers over broad fallback servers, signs short-lived per-player battle tickets with Ed25519, accepts development `SignedBattleResult` submissions after verifying version, match/mode binding, allocation player ids, result hash, replay id, settled time, battle server key id, signature shape, and idempotent replay, returns server-authoritative heartbeat presence for sessions/tickets/matches without implicit reconnect, creates server seeds, ingests monotonic tick/seq input, validates card-slot requests against server hand/energy/cooldown state, validates battle-royale card selection and Boss card-transfer mode actions, advances a deterministic stage-aware server-side bullet MVP, emits bullet deltas/snapshots, active-card snapshots, mode-action responses, and cursor-based match events, tracks Boss HP/damage/graze/hits, persists a server-owned world-boss HP snapshot with daily attempts and single defeat announcement, requires server-side Boss defeat for instance-boss clears, rejects large tick jumps, rejects input while a player is disconnected, restores players through a 30-second reconnect response with `match_start` plus full snapshot and fresh battle ticket, creates server-owned `match_end` settlements with certification rank deltas/top-30 qualification only for certification matches while PvP settlements leave certification progression unchanged, accepts post-settlement rematch confirmations from original participants, creates fresh loading rematch matches with original server loadouts/decks once all accept, and stores per-player replay audit records. Full shared SpellKard bullet/card rules, real result signatures, PostgreSQL-backed rooms/tickets, and C++ battle authority are pending. |
| Rewards and activity | Started | Server-owned rewards, wallet/task/event/leaderboard projections, certification profile projection, top-30 leaderboard claim eligibility, match settlement idempotency, activity claim idempotency, chest reward application, server-owned card upgrade cost/level/inventory flow, server-owned chest opening/cost/pity/audit flow, and client-authored reward/chest-result/upgrade-result/rank-result rejection implemented in memory. |
| Tests | Started | Go unit and HTTP integration tests cover auth, external session mapping, PhK-Protocol Go manifest version/field compatibility, bootstrap inventory/decks/chests/certification/world-boss state, inventory/deck/chest read, card upgrade cost/deduction/level projection/forbidden authority, deck save validation, chest opening/deduction/pity/audit/duplicate-dust projection, active server deck resolution for matchmaking, queue, queue-ticket cancellation/idempotency, heartbeat presence, stage/character/rating loadout validation and propagation, room-code creation/join/list/get/rules/leave/message/ticket resolution, lobby create/join/leave idempotency, host transfer, empty-room cleanup, chat/announcement permissions/idempotency/recursive authority metadata rejection, battle server registration/list/heartbeat, battle allocation selection including `pvp_duel` mode-dedicated server preference, battle ticket expiry and Ed25519 signature verification, battle lifecycle audit repository emission for allocation/ticket/result/replay plus SQL allocation/ticket/result/replay insert mapping and migration-column/key-seed coverage, reusable business envelope request adapter/guard validation/audit snapshots/audit sink behavior/SQL sink argument mapping, SDK-neutral HTTP/Nakama RPC/Nakama WSS envelope adapter construction with shared replay guard state, SDK-neutral Nakama RPC/WSS-style handler dispatch, lobby MVP protocol fields, Nakama external user/session mapping, room mode binding, ready dispatch, stale/replayed envelope rejection, client-origin Nakama `battle.result.submit` rejection plus service-origin core-validation reachability, battle result submit RPC registration source-shape checks, missing/replayed envelope rejection, Nakama binding source-shape checks, default pgx database config, migration loading/apply/skip ordering with a fake `database/sql` driver, HTTP fallback business envelope compatibility/replay/stale timestamp rejection/status projection, battle result submit route/core validation/idempotency/audit projection, ready, input, authoritative bullet deltas, server-owned active-card snapshots, server-owned Boss HP/damage fields, PvP Duel bootstrap/match/ticket/ready/input/settlement contract and no-certification-mutation guard, world-boss HP persistence/daily attempts/defeat announcement, instance-boss defeat-required settlements, cursor event polling, mode-action acceptance/rejection, disconnect/reconnect restore, disconnected-input rejection, forbidden result/card-state/rank fields, large tick jump rejection, certification rank/top-30 settlement projection, settlement/reward idempotency, rematch waiting/found/idempotency/authorization, replay record reads and authorization, and activity claims. |
| Deployment | Pending | Self-hosted local stack to be added after Nakama/PostgreSQL binding. Current service can run locally with `go run ./cmd/gensoulkyo`. |

## 2026-07-01 nakama-server-agent service callback docs sync

- Synced the Runtime stack status with the merged Nakama service-origin callback gate: service callbacks require allowlisted RPC names, no player session/user context, Nakama `rpc` mode, and canonical `runtime.RUNTIME_CTX_VARS` origin/flag values from `core.ServiceCallbackContext()`.
- Marked the legacy root checkout `agent/gensoulkyo-lobby/20260629-0900` dirty callback-gate edits as superseded by merged PR #59's stricter implementation; no legacy worktree changes were migrated.
- Preserved the authority split: this is documentation/status alignment only, with client RPC/WSS result submission and high-frequency battle ticks still forbidden on the Go/Nakama business path.
- Tightened the build-tagged Nakama callback gate regression so every accepted callback flag value comes from `core.ServiceCallbackAcceptedValues()`, keeping the SDK binding aligned with the shared business contract.
- Verified `docker-compose --profile nakama-tag-build run --rm -e GOPROXY=https://goproxy.cn,direct -e GOSUMDB=off nakama-tag-build`, `go test ./runtime/... ./cmd/gensoulkyo_nakama`, `docker-compose --profile test run --rm test`, and `python3 /root/gotouhou/docs/ops/protocol_audit_check.py`.

## 2026-06-29 gensoulkyo-lobby update

- Added a server-authoritative `BattleLifecycleAuditStatus` snapshot in `runtime/core` that counts allocation, battle ticket, battle result, and replay audit repository writes and records the last non-secret write failure.
- Exposed the snapshot through authenticated, business-envelope-protected `battle.audit.status` RPC/WSS dispatch in `runtime/nakamaapi`, and registered the RPC in the build-tagged Nakama binding.
- Kept HTTP/local fallback battle lifecycle behavior permissive: audit repository failures are surfaced for operations/CI visibility but do not make the development fallback path the production combat authority.

## 2026-06-30 nakama-server-agent business event lobby audit update

- Added lobby lifecycle audit writes for SDK-neutral Nakama `business.event` room and room-ticket state reads, so low-frequency business WSS projections leave the same durable trace as `rooms.get` and `matchmaking.ticket`.
- Extended core and DB-backed Nakama regressions to prove `business.event` room snapshots remain business-envelope protected, server-authoritative, and excluded from high-frequency battle tick or client-authored result authority.
- Verified `go test ./runtime/... ./cmd/gensoulkyo_nakama`, `docker-compose --profile test run --rm test`, and `python3 /root/gotouhou/docs/ops/protocol_audit_check.py`.

## 2026-06-29 gensoulkyo-lobby lobby audit update

- Added an injected lobby lifecycle audit repository for room create/join/match/leave/rules-read and lobby message/duplicate-message events, plus a `database/sql` PostgreSQL writer and migration tables for `lobby_room_audits` and `lobby_message_audits`.
- Wired the Nakama build-tagged module to create both battle and lobby lifecycle audit repositories from Nakama's `*sql.DB`; authenticated `lobby.audit.status` RPC/WSS now exposes whether durable lobby audit writes are configured and whether recent writes failed.
- Preserved the authority split: room lifecycle validation, forbidden client-authored fields, host-only announcements, and battle allocation/ticket/result paths stay server-owned; audit sink failures are visible in status but do not turn HTTP fallback into production authority.

## 2026-06-29 gensoulkyo-lobby Nakama DB wiring update

- Added `runtime/nakamaapi.NewWithDatabase` so SDK-neutral Nakama handlers and the build-tagged Nakama module share one PostgreSQL audit wiring path for business-envelope, battle lifecycle, and lobby lifecycle audit persistence.
- Added a handler-level SQL capture regression that drives business-envelope-protected RPC/WSS room creation, room join, battle ticket reads, and audit status reads through the DB-backed constructor, then verifies inserts reach `business_envelope_audits`, `lobby_room_audits`, `match_allocation_audits`, and `battle_ticket_audits`.
- Kept `battle.result.submit` off client WSS; the new wiring only records business/lobby/allocation/ticket audit rows and does not add a Go HTTP production combat authority path.

## 2026-06-29 gensoulkyo-lobby service-origin result guard update

- Added SDK-neutral service-origin metadata to Nakama RPC requests while leaving the build-tagged public Nakama RPC binding fail-closed until a real S2S auth signal is added.
- Rejected client-origin Nakama `battle.result.submit` calls before core dispatch while preserving an explicit service-origin path for future C++ Battle Server callback scaffolds to reach signed-result validation.
- Updated Nakama binding docs and source-shape tests to preserve the authority split: clients can read allocation/tickets/status through business RPC/WSS, but cannot use Nakama RPC/WSS as a settlement authority path.

## 2026-06-29 gensoulkyo-lobby Nakama registry verification update

- Hardened `cmd/gensoulkyo_nakama` source-shape coverage so default tests parse the build-tagged `rpcIDs` registry and require the exact lobby, battle allocation/ticket, audit status, and result-submit RPC set without duplicates.
- Added a regression guard that the public Nakama RPC binding continues to wire Nakama `*sql.DB` through `runtime/nakamaapi.NewWithDatabase` while not marking client-origin RPC calls as service-origin.
- Verified `go test ./...`, Docker Compose test profile, and the cross-repo protocol audit. Real `go test -tags nakama` is still blocked until the module/dependency scope can add and pin `github.com/heroiclabs/nakama-common/runtime`; current latest module metadata requires a newer Go toolchain than this runner.

## 2026-06-29 gensoulkyo-lobby lobby read audit update

- Added server-authoritative lobby read audit records for `rooms.list` and `rooms.get`, keeping read visibility separate from room mutation, rules-read, and message counters in `LobbyLifecycleAuditStatus`.
- Extended the PostgreSQL lobby room audit action constraint and SQL/Nakama regression coverage so DB-backed handlers record room snapshot reads through protected RPC/WSS adapters before allocation/ticket issuance.
- Verified `go test ./...`, `docker-compose --profile test run --rm test`, and `/root/gotouhou/docs/ops/protocol_audit_check.py`; `go test -tags nakama ./cmd/gensoulkyo_nakama` remains blocked by the missing `github.com/heroiclabs/nakama-common/runtime` module declaration.

## 2026-06-29 gensoulkyo-lobby allocation fallback accounting update

- Fixed battle allocation accounting so the default local fallback battle server increments active-match/load counters through the same path as registered mode-capable battle servers.
- Added a regression proving fallback allocation reports `ActiveMatches=1`, nonzero load, and does not double-count when the existing allocation is read again by another participant.
- Probed the real Nakama SDK tag build: `github.com/heroiclabs/nakama-common@v1.34.0` is the newest tested module baseline here that still supports the repository's Go 1.20 Docker image; newer tags require Go 1.23+ or later. A temporary local module pin made `go test -tags nakama ./cmd/gensoulkyo_nakama ./runtime/...` and `go build -tags nakama -buildmode=plugin` pass, but the durable `go.mod`/`go.sum` pin is outside this scope's allowed paths, so it was not kept in this scoped change.
- Verified `go test ./runtime/core -run 'TestBattleAllocationFallbackAccountsServerLoad|TestBattleAllocationAndSignedTicketUseRegisteredServer|TestPvPDuelModeContractAllocationAndSettlement'`, `go test ./...`, `docker-compose --profile test run --rm test`, and `/root/gotouhou/docs/ops/protocol_audit_check.py`. Current in-scope tree still reports `go test -tags nakama ./cmd/gensoulkyo_nakama ./runtime/...` blocked by the missing `github.com/heroiclabs/nakama-common/runtime` module declaration.

## 2026-06-29 gensoulkyo-lobby room cancellation audit update

- Wired explicit `CancelTicket` room cancellations into the existing lobby lifecycle audit repository using the already-migrated `cancelled` action, covering both empty-room host cancellation and member cancellation from a waiting room.
- Added a regression that queue-only cancellation remains local queue state while room-ticket cancellation emits server-authoritative lobby audit records and increments `LobbyLifecycleAuditStatus.RoomRecords`.
- Verified `go test ./runtime/core -run TestCancelTicketRemovesQueueAndRoomWait -count=1`, `go test ./...`, `docker-compose --profile test run --rm test`, and `/root/gotouhou/docs/ops/protocol_audit_check.py`.

## 2026-06-29 gensoulkyo-lobby duplicate lobby message audit update

- Extended the DB-backed Nakama handler regression to send an original and duplicate `rooms.message` WSS event through `runtime/nakamaapi.NewWithDatabase`.
- Verified duplicate lobby message idempotency now has durable SQL coverage: `lobby_message_audits` receives both rows, the duplicate row preserves the duplicate/server-authoritative flags, and `LobbyLifecycleAuditStatus.MessageRecords` increments for both writes.
- This remains lobby/business audit coverage only; client-origin result submission is still rejected and no HTTP fallback path becomes production combat authority.

## 2026-06-29 gensoulkyo-lobby Nakama replay read update

- Exposed `replay.get` through the SDK-neutral Nakama RPC and WSS adapters, requiring the same business envelope guard as other authenticated business reads and delegating authorization to the server-owned `Replay` path.
- Registered `replay.get` in the build-tagged Nakama RPC source and extended source-shape tests so replay reads stay in the exact RPC registry while `battle.result.submit` remains service-origin only.
- Added a Nakama adapter regression that accepts a service-origin battle result, derives the server-authoritative replay id from duplicate settlement state, rejects prefix/empty/cross-user replay reads, and returns the owner's replay with the C++ callback replay marker.
- Verified `go test ./runtime/nakamaapi -run TestNakamaReplayReadRequiresEnvelopeAndOwner -count=1`, `go test ./cmd/gensoulkyo_nakama -run 'TestNakamaBinding' -count=1`, `go test ./...`, `docker-compose --profile test run --rm test`, and `python3 /root/gotouhou/docs/ops/protocol_audit_check.py`. Direct execution of the protocol audit script is blocked by its non-executable file mode, and `go test -tags nakama ./cmd/gensoulkyo_nakama ./runtime/...` remains blocked by the missing `github.com/heroiclabs/nakama-common/runtime` module declaration.

## 2026-06-29 gensoulkyo-lobby room ticket read audit update

- Added server-authoritative lobby read audit records for room-backed `matchmaking.ticket` polling so Nakama RPC/WSS clients reading a waiting-room ticket leave a PostgreSQL-compatible lifecycle trace without mutating room state.
- Extended `LobbyLifecycleAuditStatus` and the PostgreSQL `lobby_room_audits.action` constraint with `ticket_read`, keeping ticket polling counted with read visibility instead of room mutation/message/rules counters.
- Verified `go test ./runtime/core ./runtime/nakamaapi ./cmd/gensoulkyo_nakama`, `go test ./...`, `docker-compose --profile test run --rm test`, and `python3 /root/gotouhou/docs/ops/protocol_audit_check.py`. Direct `docker compose --profile ...` is unavailable on this host, and direct execution of the protocol audit script is blocked by file mode, so `docker-compose` and `python3` were used.

## 2026-06-29 gensoulkyo-lobby Nakama payload guard update

- Hardened the build-tagged Nakama RPC binding so malformed JSON payloads are reported to `runtime/nakamaapi` as explicit parse errors instead of being wrapped as fallback body data.
- Added SDK-neutral handler coverage proving malformed binding payloads fail as `invalid_request` before login, envelope validation, or business dispatch; source-shape tests now require this guard to stay wired in the real Nakama binding.
- Verified `go test ./runtime/nakamaapi ./cmd/gensoulkyo_nakama`; full Go, Docker Compose, and protocol audit checks are rerun for this final sample below. The real `go test -tags nakama` path remains blocked by the missing `github.com/heroiclabs/nakama-common/runtime` module declaration outside this scope's allowed paths.

## 2026-06-29 gensoulkyo-lobby service callback context guard update

- Tightened SDK-neutral Nakama `battle.result.submit` handling so service-origin callbacks are rejected if they also carry player `session_id` or `user_id` context, preventing a miswired public RPC from looking like a C++ Battle Server settlement callback.
- Added handler regression coverage for the new fail-closed path while preserving the context-free service-origin scaffold that reaches core signed-result validation, and pinned the build-tagged Nakama source-shape test to keep forwarding user context into `runtime/nakamaapi`.
- Verified `go test ./...`, `docker-compose --profile test run --rm test`, and `python3 /root/gotouhou/docs/ops/protocol_audit_check.py`. Direct `docker compose` is unavailable on this host; `go test -tags nakama ./cmd/gensoulkyo_nakama` remains blocked by the missing `github.com/heroiclabs/nakama-common/runtime` module declaration outside this scope's allowed paths.

## 2026-06-29 gensoulkyo-lobby lifecycle status fingerprint update

- Extended `BattleLifecycleAuditStatus` and `LobbyLifecycleAuditStatus` with non-secret last-success operation, timestamp, and deterministic fingerprint fields so Nakama status RPC/WSS checks can prove real lifecycle audit writes occurred.
- Added core and DB-backed Nakama handler regressions covering the fingerprints after allocation/ticket/result/replay writes and after lobby room/read/message/match writes.
- The fingerprint is derived from existing ids and hashes, not raw payloads, signatures, or secrets; it keeps audit visibility operational while preserving the Nakama/Go business authority split.

## 2026-06-29 gensoulkyo-lobby WSS ticket polling update

- Added SDK-neutral Nakama business WSS dispatch for `matchmaking.ticket`, matching the existing RPC route so room and match progress polling can stay on the intended business socket path.
- Extended Nakama adapter regressions to prove WSS ticket polling requires the shared business envelope, returns the server-owned match allocation and signed battle ticket, and does not create fake audit progress when durable lobby repositories are not configured.
- Extended the DB-backed handler regression so configured SQL repositories record WSS room-ticket polling as the existing `ticket_read` lobby lifecycle audit, with status fingerprints reflecting the durable read.

## 2026-06-29 gensoulkyo-lobby WSS battle server discovery update

- Added SDK-neutral Nakama business WSS dispatch for `battle.servers`, matching the existing RPC route so lobby clients can inspect server-authoritative battle server discovery through the business socket path.
- Extended Nakama lobby adapter coverage to prove WSS battle server discovery is business-envelope protected and returns a server-authoritative `BattleServerListResponse`.
- This remains discovery/status parity only: allocation, ticket issuance, and result submission authority are unchanged, and client-origin result submission remains rejected.

## 2026-06-29 gensoulkyo-lobby migration documentation guard update

- Updated the migration README to list the concrete PostgreSQL audit tables created by `001_business_security_audit`, including lobby room/message lifecycle tables now used by the Nakama database-backed handler path.
- Added a regression in the core migration/repository test so the operational migration README must continue documenting the table identifiers used by envelope, battle lifecycle, replay, and lobby lifecycle audit wiring.
- Verified `go test ./runtime/core -run TestBattleLifecycleAuditMigrationMatchesRepositoryTables -count=1`, `go test ./...`, `docker-compose --profile test run --rm test`, and `python3 /root/gotouhou/docs/ops/protocol_audit_check.py`. `go test -tags nakama ./cmd/gensoulkyo_nakama ./runtime/...` remains blocked by the missing `github.com/heroiclabs/nakama-common/runtime` module declaration outside this scope's allowed paths.

## 2026-06-29 gensoulkyo-lobby Nakama battle server lifecycle update

- Exposed `battle.servers.register` and `battle.servers.heartbeat` through the SDK-neutral Nakama RPC adapter as service-origin-only calls, reusing the existing core battle server registration and heartbeat lifecycle methods.
- Kept player/client authority closed: public Nakama RPC calls and miswired service-origin calls carrying player `session_id` or `user_id` are rejected before registration/heartbeat dispatch, while authenticated clients can still read `battle.servers` through the business envelope guard.
- Registered the new service-origin RPC ids in the build-tagged Nakama module and extended source-shape/handler regressions so battle server discovery, registration, heartbeat, allocation, ticket, and result-submit boundaries stay explicit.
- Verified `go test ./runtime/nakamaapi -run TestNakamaBattleServerRegisterAndHeartbeatRequireServiceOrigin -count=1`, `go test ./cmd/gensoulkyo_nakama -run 'TestNakamaBinding' -count=1`, `go test ./...`, `docker-compose --profile test run --rm test`, and `python3 /root/gotouhou/docs/ops/protocol_audit_check.py`. `go test -tags nakama ./cmd/gensoulkyo_nakama ./runtime/...` remains blocked by the missing `github.com/heroiclabs/nakama-common/runtime` module declaration outside this scope's allowed paths.

## 2026-06-29 gensoulkyo-lobby Nakama tag-build compose update

- Added a `nakama-tag-build` Docker Compose profile that copies the repository into a temporary container workspace, applies the local PhK-Protocol replace, temporarily pins `github.com/heroiclabs/nakama-common/runtime` through `NAKAMA_COMMON_VERSION` (default `v1.34.0`), then runs `go test -tags nakama ./cmd/gensoulkyo_nakama ./runtime/...` and builds the Nakama Go Runtime plugin.
- Documented the profile in `cmd/gensoulkyo_nakama/README.md` so SDK tag-build validation no longer requires editing in-scope files or leaving `go.mod`/`go.sum` changes behind; the compose service now forwards `GOSUMDB` alongside `GOPROXY` so restricted fallback runners can use explicit module-fetch overrides.
- Validated the real Nakama tag-build path in Docker with the pinned SDK baseline: the default `proxy.golang.org` and direct checksum paths timed out on external module fetches, then `docker-compose --profile nakama-tag-build run --rm -e GOPROXY=https://goproxy.cn,direct -e GOSUMDB=off nakama-tag-build` completed `go test -tags nakama ./cmd/gensoulkyo_nakama ./runtime/...` and `go build -tags nakama -buildmode=plugin`.
- Added default source-shape coverage that pins the compose profile, README fallback command, and temporary SDK pin contract while the durable `go.mod`/`go.sum` Nakama dependency remains outside this scope's allowed paths.

## 2026-06-29 gensoulkyo-lobby battle server lifecycle audit update

- Added PostgreSQL-compatible audit records for service-origin Nakama `battle.servers.register` and `battle.servers.heartbeat` callbacks using the existing battle lifecycle repository path.
- Extended `BattleLifecycleAuditStatus` with `server_lifecycle_records` so operators can distinguish battle gateway registration/heartbeat writes from match allocation, ticket, result, and replay audit rows.
- Kept the boundary closed to clients: public/miswired Nakama RPC calls still require service origin and cannot become combat authority or settlement authority.

## 2026-06-29 gensoulkyo-lobby battle server offline lifecycle update

- Added a service-origin-only `battle.servers.offline` Nakama RPC path that marks an existing battle server unavailable, preserves its discovery metadata, resets reported load, and keeps future allocations on online servers.
- Recorded offline transitions through the same PostgreSQL-compatible battle lifecycle audit repository and migration status constraint as register/heartbeat callbacks.
- Kept public/client calls fail-closed: authenticated players can still read `battle.servers` through the business envelope guard, but cannot register, heartbeat, offline, or submit settlement callbacks.

## 2026-06-29 gensoulkyo-lobby battle server heartbeat freshness update

- Added a 30-second registered battle server heartbeat freshness gate for new match allocations so stale-but-not-explicitly-offline servers are skipped before issuing battle tickets.
- Preserved existing allocation stability: once a match has a server allocation, later heartbeat staleness does not silently move that match to another endpoint.
- Kept the local default fallback eligible for development contract tests while registered C++ battle server candidates must stay online and fresh to receive new allocations.

## 2026-06-29 gensoulkyo-lobby lobby message idempotency update

- Scoped lobby message idempotency to `room_code + user_id + message_id`, matching the PostgreSQL audit uniqueness target and preventing one participant from colliding with another participant's client-generated message id.
- Added regression coverage that a cross-user message-id collision creates a new server-authoritative message while same-user retries remain duplicate/idempotent audit records.
- Added a host-leave lifecycle regression proving the remaining queued participant is visible as the promoted host and is the only player allowed to publish host announcements.

## 2026-06-29 gensoulkyo-lobby duplicate result callback audit update

- Added a `duplicate` battle result audit status and a separate `result_duplicate_records` lifecycle counter so repeated C++ Battle Server result callbacks are durable and visible without counting as new settlements.
- Updated the PostgreSQL `battle_result_audits` draft to keep one accepted row per match while allowing one idempotent duplicate callback marker per match/result hash, and wired the SQL repository to persist the callback status.
- Extended core and DB-backed Nakama handler regressions so duplicate service-origin `battle.result.submit` calls return authoritative idempotent receipts, write a duplicate result audit row, and do not create extra replay audit rows or reopen client settlement authority.
- Verified `go test ./...`, `docker-compose --profile test run --rm test`, `python3 /root/gotouhou/docs/ops/protocol_audit_check.py`, and `docker-compose --profile nakama-tag-build run --rm -e GOPROXY=https://goproxy.cn,direct -e GOSUMDB=off nakama-tag-build`.

## 2026-06-29 gensoulkyo-lobby idempotent lobby retry audit update

- Added PostgreSQL-compatible `create_retry` and `join_retry` room lifecycle audit actions for idempotent room create/join retries, counted as read/visibility records rather than new room mutations.
- Extended the core lobby audit regression and migration action guard so duplicate lobby lifecycle calls leave a durable non-secret fingerprint without creating extra tickets, rooms, or player authority.
- This preserves the Nakama/Go authority split: retry audits improve lobby observability only, while battle allocation, ticket issuance, and result submission boundaries remain unchanged.
- Verified `go test ./...`, `docker-compose --profile test run --rm test`, `python3 /root/gotouhou/docs/ops/protocol_audit_check.py`, and `docker-compose --profile nakama-tag-build run --rm -e GOPROXY=https://goproxy.cn,direct -e GOSUMDB=off nakama-tag-build`.

## 2026-06-29 gensoulkyo-lobby room ready audit update

- Added PostgreSQL-compatible `ready` lobby lifecycle audit records for first-time room-backed `match.ready` transitions, with a separate `ready_records` status counter so operators can distinguish player readiness from room mutations and reads.
- Kept ready retries idempotent: duplicate `match.ready` calls still return the authoritative running state and do not write extra ready audit rows.
- Extended core and DB-backed Nakama handler regressions so RPC/WSS ready dispatch records durable room lifecycle visibility while battle allocation, ticket issuance, and result submission authority remain unchanged.

## 2026-06-29 gensoulkyo-lobby reconnect audit update

- Exposed `match.disconnect` and `match.reconnect` through the SDK-neutral Nakama RPC/WSS adapter and registered the build-tagged Nakama RPC ids, using the existing server-owned reconnect methods.
- Added PostgreSQL-compatible `disconnected` and `reconnected` lobby lifecycle audit records for room-backed running matches, plus a `connection_records` status counter; duplicate disconnect/reconnect calls do not inflate lifecycle audit counts.
- Kept the authority split intact: clients can request reconnect lifecycle changes through business envelope-protected Nakama RPC/WSS, while snapshots, signed battle tickets, allocation, replay, and settlement remain server-owned.

## 2026-06-30 gensoulkyo-lobby WSS callback security audit update

- Kept service-origin-only Nakama WSS callback names fail-closed before player session mapping, envelope validation, or core dispatch, so clients still cannot register battle servers, heartbeat/offline them, or submit settlement callbacks through WSS.
- Added a synthetic non-secret rejected business-envelope audit for those forbidden WSS attempts, preserving operational visibility without accepting the supplied envelope or consuming the player's replay seq/nonce state.
- Verified the focused SDK-neutral Nakama adapter regression with `go test ./runtime/nakamaapi -run TestNakamaWSSRejectsServiceOriginOnlyCallbacksBeforeReplayState -count=1`; broader Go, Docker Compose, Nakama tag-build, and protocol audit checks are rerun for this final sample.

## 2026-06-30 gensoulkyo-lobby presence heartbeat audit update

- Added PostgreSQL-compatible `heartbeat` lobby lifecycle audit records for room-backed and match-backed `presence.heartbeat` calls, counted under `connection_records` so liveness visibility stays separate from room mutations and ready transitions.
- Extended core and DB-backed Nakama regressions so authenticated RPC/WSS heartbeats write durable lobby audit rows through `runtime/nakamaapi.NewWithDatabase`, while allocation, battle tickets, replay, and service-origin battle result authority remain unchanged.
- Updated the `lobby_room_audits.action` migration constraint and migration guard to keep the heartbeat audit contract aligned with the SQL repository path.
- Verified `go test ./...`, `docker-compose --profile test run --rm test`, `python3 /root/gotouhou/docs/ops/protocol_audit_check.py`, and `docker-compose --profile nakama-tag-build run --rm -e GOPROXY=https://goproxy.cn,direct -e GOSUMDB=off nakama-tag-build`.

## 2026-06-30 gensoulkyo-lobby battle ticket consume audit update

- Added a service-origin-only Nakama `battle.ticket.consume` RPC for C++ Battle Server ticket acceptance callbacks, reusing the existing signed ticket fields and `consumed` PostgreSQL audit status.
- Core ticket consumption now validates ticket id, match id, battle server id, nonce, and optional user/player binding, records one durable consumed transition, treats repeated consume callbacks as idempotent, and issues a fresh short-lived ticket on later client reads instead of reusing the consumed ticket.
- Kept the authority split closed to clients: business RPC/WSS clients can still read allocation/tickets, but public `battle.ticket.consume` calls and all callback names over WSS remain rejected before core dispatch.

## 2026-06-30 gensoulkyo-lobby offline callback canonicalization update

- Hardened `BattleServerOffline` so service-origin offline callbacks always force the canonical `offline` state and cannot preserve or smuggle an `online` status from payload data.
- Added core and SDK-neutral Nakama adapter regressions proving malicious or misconfigured offline payloads still reset load, preserve discovery metadata, and keep the server out of future allocation eligibility.
- This stays within the Nakama/Go business lifecycle boundary: clients still cannot call battle server lifecycle callbacks, and no HTTP/Nakama path becomes combat simulation or settlement authority.

## 2026-06-30 gensoulkyo-lobby register heartbeat canonicalization update

- Hardened battle server `register` and `heartbeat` lifecycle callbacks so service-origin payload status is canonicalized to `online`; `offline` remains available only through the explicit `battle.servers.offline` callback.
- Extended core and SDK-neutral Nakama regressions so malformed `offline`/`draining` register-heartbeat payloads cannot leak into discovery status or PostgreSQL-compatible lifecycle audit metadata.
- Verified `go test ./...`, `docker-compose --profile test run --rm test`, `python3 /root/gotouhou/docs/ops/protocol_audit_check.py`, and `docker-compose --profile nakama-tag-build run --rm -e GOPROXY=https://goproxy.cn,direct -e GOSUMDB=off nakama-tag-build`.

## 2026-06-30 gensoulkyo-lobby WSS ticket-consume guard update

- Extended the SDK-neutral Nakama WSS callback regression so `battle.ticket.consume` is covered with the other service-origin-only callback names.
- This pins the intended boundary after ticket-consume wiring: clients may read battle tickets through business RPC/WSS, but ticket acceptance callbacks remain RPC service-origin-only and WSS attempts are rejected before envelope replay state is consumed.
- Verified `go test ./runtime/nakamaapi -run TestNakamaWSSRejectsServiceOriginOnlyCallbacksBeforeReplayState -count=1`, `go test ./...`, `docker-compose --profile test run --rm test`, `python3 /root/gotouhou/docs/ops/protocol_audit_check.py`, and `docker-compose --profile nakama-tag-build run --rm -e GOPROXY=https://goproxy.cn,direct -e GOSUMDB=off nakama-tag-build`.

## 2026-06-30 gensoulkyo-lobby Nakama tag-build validation refresh

- Re-ran the disposable Docker Compose `nakama-tag-build` profile against `github.com/heroiclabs/nakama-common/runtime@v1.34.0`, keeping the temporary SDK pin outside repository `go.mod`/`go.sum` while validating the real `nakama` build tag path.
- Confirmed the profile completes `go test -tags nakama ./cmd/gensoulkyo_nakama ./runtime/...` and `go build -tags nakama -buildmode=plugin` when the runner uses the documented disposable module-fetch override `GOPROXY=https://goproxy.cn,direct` with `GOSUMDB=off`; the default `proxy.golang.org` path timed out before compilation in this runner.
- This refresh only validates the business-server Nakama SDK binding and plugin artifact path. It does not add a production HTTP combat authority path, and the durable dependency pin/Go baseline decision remains outside this scope's allowed files.

## 2026-06-30 gensoulkyo-lobby service callback envelope-shape guard update

- Hardened service-origin-only Nakama RPC callbacks so payloads carrying the client/business `business_envelope` wrapper are rejected before core dispatch.
- Added SDK-neutral regression coverage across battle server register/heartbeat/offline, ticket consume, and result submit callbacks proving an envelope-shaped service callback does not consume business replay guard seq/nonce state and cannot reach ticket/result/battle-server callback validation as if it were a C++ Battle Server message.
- This preserves the split: authenticated clients still use envelope-protected business RPC/WSS reads and lobby actions, while service callbacks stay a separate Nakama RPC origin path.

## 2026-06-30 gensoulkyo-lobby room rules tick-boundary update

- Added `high_frequency_battle_tick_allowed=false` to `RoomRulesSnapshot`, aligning the room/rules contract with `BusinessEvent` so clients can distinguish low-frequency Nakama HTTPS/WSS lobby notifications from C++ Battle Server KCP tick traffic.
- Extended core, HTTP fallback, and SDK-neutral Nakama room-rule regressions so the contract keeps business envelope required, client result submission forbidden, and high-frequency battle ticks forbidden on the business channel.
- Verified `go test ./runtime/... ./cmd/gensoulkyo_nakama`, `docker-compose --profile test run --rm test`, and `python3 /root/gotouhou/docs/ops/protocol_audit_check.py`.

## 2026-06-30 gensoulkyo-lobby client version stamp update

- Tightened match entry version validation so an omitted `client_version` remains a development fallback, but any supplied version stamp must include matching protocol, business API, battle API, and ruleset versions before queue or room entry.
- Added core, HTTP fallback, and SDK-neutral Nakama regressions proving partial version stamps are rejected for matchmaking and room joins while complete current stamps still pass.
- Verified `go test ./runtime/... ./cmd/gensoulkyo_nakama`, `docker-compose --profile test run --rm test`, and `python3 /root/gotouhou/docs/ops/protocol_audit_check.py`.

## 2026-06-30 gensoulkyo-lobby Nakama callback context fail-closed update

- Hardened the build-tagged Nakama service-origin RPC gate so missing or empty canonical callback context values from `core.ServiceCallbackContext()` fail closed before battle server lifecycle, ticket-consume, or result-submit callbacks can reach core dispatch.
- Kept the gate tied to explicit Nakama `rpc` mode, no player session/user context, allowlisted callback names, and `gensoulkyo_service_origin=battle_server` plus accepted callback markers; this does not create any high-frequency tick or client settlement authority path.
- Dispositioned the legacy checkout dirty set in `/root/gotouhou/Gensoulkyo`: those four Nakama callback-gate edits were valuable but already superseded by the current managed branch implementation and this narrower hardening.
- Verified `go test ./runtime/... ./cmd/gensoulkyo_nakama`, `docker-compose --profile test run --rm test`, and `python3 /root/gotouhou/docs/ops/protocol_audit_check.py`.

## 2026-06-30 gensoulkyo-lobby service callback context contract update

- Tightened the core operation-contract regression so `ServiceCallbackContext()` must expose exactly the required non-secret gate fields and no blank keys or values.
- This keeps room rules/business-event callback metadata aligned with the build-tagged Nakama service-origin gate without broadening client RPC/WSS authority.
- Verified `go test ./runtime/core -run TestBusinessOperationContractsKeepServiceCallbacksOutOfClientList -count=1`, `go test ./runtime/... ./cmd/gensoulkyo_nakama`, `docker-compose --profile test run --rm test`, and `python3 /root/gotouhou/docs/ops/protocol_audit_check.py`.

## 2026-06-30 nakama-server-agent HTTP service callback envelope-shape update

- Aligned HTTP fallback service-origin callback payload guarding with the Nakama RPC guard so scalar business-envelope `version` fields are rejected before battle server lifecycle, ticket-consume, or result-submit dispatch.
- Preserved structured protocol version stamps for legitimate service callbacks; this only hardens the development fallback boundary and does not create high-frequency tick or client settlement authority.
- Verified `go test ./runtime/httpapi -run 'TestHTTPServiceCallbacksRejectBusinessEnvelopePayloadShape|TestHTTPBattleTicketConsumeAndResultSubmitFallback' -count=1`, `go test ./runtime/... ./cmd/gensoulkyo_nakama`, `docker-compose --profile test run --rm test`, and `python3 /root/gotouhou/docs/ops/protocol_audit_check.py`.
