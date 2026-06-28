# migrations

PostgreSQL migration drafts for the planned Nakama/Go business server.

Current files:

- `001_business_security_audit.up.sql`: business envelope key/version registry, envelope audit log, nonce-window checkpoint table, battle ticket audit table, and match allocation audit table.
- `001_business_security_audit.down.sql`: rollback for the same draft tables.

These migrations are not wired into the in-memory HTTP MVP yet. They define the persistence target for `runtime/security` audit events, future Nakama RPC/WSS envelope validation, battle ticket issuance, and match allocation bookkeeping.

`runtime/security.NewSQLBusinessEnvelopeAuditSink` already provides a standard-library `database/sql` writer for `business_envelope_audits`. `runtime/storage` can open an explicitly configured database and apply pending `.up.sql` files in version order. `cmd/gensoulkyo` registers the pgx driver as `pgx`; broader repository wiring and deployment automation are still pending.
