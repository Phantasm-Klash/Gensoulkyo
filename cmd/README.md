# cmd

Service entrypoints.

- `gensoulkyo`: local HTTP service for the current in-memory MVP.

Run:

```powershell
go run ./cmd/gensoulkyo -addr 127.0.0.1:7350
```

Optional persistence flags:

```powershell
go run ./cmd/gensoulkyo -database-url postgres://phantasm:phantasm@localhost:5432/gensoulkyo?sslmode=disable -migrate-up
```

The open-source MVP registers the pgx `database/sql` driver as `pgx`; `-database-driver` can still override it for custom deployment builds.
