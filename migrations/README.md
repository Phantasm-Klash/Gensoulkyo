# migrations

PostgreSQL migration drafts for the planned Nakama/Go business server.

Current files:

- `001_business_security_audit.up.sql`: creates `business_envelope_keys`, `business_envelope_audits`, `business_envelope_nonce_windows`, `battle_ticket_audits`, `match_allocation_audits`, `battle_result_audits`, `replay_audits`, `lobby_room_audits`, and `lobby_message_audits`.
- `001_business_security_audit.down.sql`: rollback for the same draft tables.

These migrations define the persistence target for `runtime/security` audit events, Nakama RPC/WSS envelope validation, battle server registration/heartbeat/offline lifecycle rows, battle ticket issuance and match allocation bookkeeping with protocol/business/battle/ruleset version stamps, accepted and duplicate battle result callback audits, replay audit records, and lobby room/message lifecycle audit records.

`runtime/security.NewSQLBusinessEnvelopeAuditSink` already provides a standard-library `database/sql` writer for `business_envelope_audits`. `runtime/core` provides standard-library `database/sql` writers for battle server lifecycle/allocation/ticket/result/replay audits and lobby room/message lifecycle audits. `runtime/storage` can open an explicitly configured database and apply pending `.up.sql` files in version order. `cmd/gensoulkyo` registers the pgx driver as `pgx`; broader repository wiring and deployment automation are still pending.
