# tests

Current automated coverage lives beside the Go packages:

- `runtime/core/service_test.go` covers anonymous login, deck validation, stage/character loadout validation, mode/stage matchmaking buckets, pre-match ticket cancellation, heartbeat presence for queues/rooms/matches/disconnected players, room-code creation/join/ticket resolution, lobby message idempotency/audit/host announcements, ready/start, input ingestion, deterministic bullet deltas, validated card requests, active-card snapshots, cursor event polling, battle-royale and Boss mode-action validation, world-boss global HP/attempt/announcement persistence, instance-boss server-side clear requirements, disconnect/reconnect restore, disconnected-input rejection, server-owned Boss HP/damage fields, large tick jump rejection, forbidden client result/card-state fields, server-owned settlement, replay record reads and authorization, reward idempotency, task/event/leaderboard projection, and activity claim idempotency.
- `runtime/httpapi/handler_test.go` covers the HTTP contract for auth, bootstrap, heartbeat presence, queue, ticket cancellation, server-confirmed loadouts, room-code create/list/get/rules/join/leave/message flows, ready, input, authoritative bullet and active-card snapshot shape, event polling, mode-action submission, disconnect/reconnect, settlement, replay reads, and forbidden reward/authority-field rejection.

Run from the repository root:

```powershell
go test ./...
```

Replay determinism tests and PostgreSQL integration tests are still pending.
