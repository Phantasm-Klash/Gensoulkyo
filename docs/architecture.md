# Architecture

Gensoulkyo is planned as the authoritative server for Phantasm Klash online play.

The first implementation target is a self-hosted server that supports:

- player account creation and login without commercial platform dependencies;
- lobby and room creation;
- 1v1 match sessions;
- tick-based input ingestion;
- server-owned random seeds;
- validated card activation requests;
- match snapshots;
- replay metadata persistence.

The client submits intent. The server decides authoritative state.

