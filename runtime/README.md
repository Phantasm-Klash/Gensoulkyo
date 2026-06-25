# runtime

Nakama Go runtime for Gensoulkyo.

Packages:

- `server`: Nakama `InitModule`, RPC registration, and match registration.
- `match`: authoritative tick state, input ingestion, snapshots, match persistence, and idempotent local reward settlement.
- `decks`: PostgreSQL-backed deck save/list/active-snapshot validation.
- `cards`: card catalog and deck validation primitives.
- `economy`: deterministic local chest-opening primitive for the open server.
- `config`: versioned open-alpha runtime settings.

Registered RPCs:

- `gensoulkyo.config.get`
- `gensoulkyo.profile.get`
- `gensoulkyo.deck.save`
- `gensoulkyo.deck.list`
- `gensoulkyo.inventory.get`
- `gensoulkyo.chest.pool.list`
- `gensoulkyo.chest.open`
- `gensoulkyo.room.create`
- `gensoulkyo.room.join`
- `gensoulkyo.room.start`
- `gensoulkyo.match.create`

Deck and match behavior:

- `gensoulkyo.deck.save` persists to `player_decks`, validates the 20-card/2-copy open-alpha deck limits against enabled `cards` joined through `player_card_inventory`, and clears any previous active deck when a new active deck is saved.
- Empty local-dev inventories keep the open-alpha starter fallback so fresh self-hosted databases can create test decks before economy seeding is complete.
- `gensoulkyo.room.create` and `gensoulkyo.room.join` provide the current short-code lobby bridge for `duel` while full matchmaking queues are still future work.
- `gensoulkyo.room.start` and direct `gensoulkyo.match.create` validate every participant's active deck, create `gensoulkyo.duel`, and pass locked deck snapshots through match params.
- `gensoulkyo.duel` rejects realtime joins from users outside the locked participant set, except for diagnostic matches created without participants.
- Input packets are checked for monotonic tick/sequence, direction mask, card-slot range, and server-owned bomb resources. Invalid packets are discarded and audited.
- Repeated fallback input preserves continuous movement/shoot state but strips one-shot bomb and card requests.

Match persistence:

- `gensoulkyo.duel` creates `matches` and `match_players` when all players are ready.
- `match_players.deck_snapshot_json` stores the validated active deck snapshot supplied at match creation.
- Ready, disconnect, reconnect, bomb/card request, invalid input, state-hash checkpoint, and match-end records are written to `match_events`.
- Completion updates final player score/stat/result rows and writes `match_reward_settlements` once per `(match_id, user_id)` before crediting the local open-server wallet.
- Match-end snapshots include server-computed result and reward summaries so clients do not infer settlement locally.
- Database writes are logged from the realtime handler; transient persistence failures do not crash the active match loop.

Build as a Nakama plugin:

```sh
ALL_PROXY=socks5://10.10.10.108:10808 HTTPS_PROXY=socks5://10.10.10.108:10808 HTTP_PROXY=socks5://10.10.10.108:10808 docker build --build-arg ALL_PROXY=socks5://10.10.10.108:10808 --build-arg HTTPS_PROXY=socks5://10.10.10.108:10808 --build-arg HTTP_PROXY=socks5://10.10.10.108:10808 -f deployments/local/runtime-builder.Dockerfile -t gensoulkyo-runtime .
```
