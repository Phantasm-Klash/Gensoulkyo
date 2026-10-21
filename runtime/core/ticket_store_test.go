package core

import (
	"database/sql/driver"
	"errors"
	"strings"
	"testing"
	"time"
)

func ticketRowColumns() []string {
	return []string{
		"ticket_id", "match_id", "user_id", "player_id", "mode_id",
		"battle_server_id", "endpoint", "key_id", "signature_hex", "nonce_hex",
		"ruleset_version", "protocol_version", "business_api_version",
		"battle_api_version", "mode_config_hash", "deck_snapshot_hash",
		"ticket_json", "status", "reject_reason", "issued_at", "expires_at",
		"consumed_at", "updated_at", "server_authoritative",
	}
}

func ticketRowValues(status string, consumed any) []driver.Value {
	return []driver.Value{
		"t-1",              // ticket_id
		"m-1",              // match_id
		"u-1",              // user_id
		"p-1",              // player_id
		"pvp",              // mode_id
		"bs-104-1",         // battle_server_id
		"104.233.217.232:9001", // endpoint
		"dev-ed25519-0",    // key_id
		"deadbeef",         // signature_hex
		"cafe01",           // nonce_hex
		"mvp-boss-race-s0", // ruleset_version
		"v1",               // protocol_version
		"v1",               // business_api_version
		"v1",               // battle_api_version
		"sha256:mode",      // mode_config_hash
		"sha256:deck",      // deck_snapshot_hash
		`{"ticket_id":"t-1"}`, // ticket_json
		status,             // status
		"",                 // reject_reason
		time.Unix(1700000000, 0).UTC(), // issued_at
		time.Unix(1700000600, 0).UTC(), // expires_at
		consumed,                       // consumed_at
		time.Unix(1700000010, 0).UTC(), // updated_at
		true,                           // server_authoritative
	}
}

func TestSQLBattleTicketStoreIssueStoresTheSignedEnvelope(t *testing.T) {
	capture, name := registerStoreCaptureDriver(t)
	store, err := NewSQLBattleTicketStore(openCaptureDB(t, name))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	ticket := BattleTicketRecord{
		TicketID:            "t-1",
		MatchID:             "m-1",
		UserID:              "u-1",
		PlayerID:            "p-1",
		BattleServerID:      "bs-104-1",
		KeyID:               "dev-ed25519-0",
		SignatureHex:        "deadbeef",
		NonceHex:            "cafe01",
		TicketJSON:          `{"ticket_id":"t-1"}`,
		ExpiresAt:           time.Unix(1700000600, 0).UTC(),
		ServerAuthoritative: true,
	}
	if err := store.Issue(ticket); err != nil {
		t.Fatalf("issue: %v", err)
	}
	calls := capture.execCalls()
	if len(calls) != 1 {
		t.Fatalf("expected one exec, got %d", len(calls))
	}
	if !strings.Contains(calls[0].query, "INSERT INTO battle_tickets") {
		t.Fatalf("query = %s", calls[0].query)
	}
	if len(calls[0].args) != 24 {
		t.Fatalf("expected 24 args, got %d", len(calls[0].args))
	}
	if calls[0].args[0] != "t-1" || calls[0].args[8] != "deadbeef" || calls[0].args[9] != "cafe01" {
		t.Fatalf("args = %v", calls[0].args)
	}
	if calls[0].args[17] != TicketStatusIssued {
		t.Fatalf("status arg = %v", calls[0].args[17])
	}
	if calls[0].args[23] != true {
		t.Fatalf("server authoritative arg = %v", calls[0].args[23])
	}
}

func TestSQLBattleTicketStoreConsumeGuardsTheIssuedState(t *testing.T) {
	capture, name := registerStoreCaptureDriver(t)
	store, _ := NewSQLBattleTicketStore(openCaptureDB(t, name))

	// A ticket still in 'issued' consumes.
	if err := store.Consume("t-1", time.Unix(1700000100, 0).UTC()); err != nil {
		t.Fatalf("first consume: %v", err)
	}
	calls := capture.execCalls()
	if len(calls) != 1 || !strings.Contains(calls[0].query, "AND status = 'issued'") {
		t.Fatalf("consume must be guarded by the status: %v", calls)
	}

	// The status guard makes the second attempt affect no rows.
	capture.setAffected(0)
	capture.queueResults([]storeRow{{columns: ticketRowColumns(), values: ticketRowValues(TicketStatusConsumed, time.Unix(1700000100, 0).UTC())}})
	err := store.Consume("t-1", time.Unix(1700000200, 0).UTC())
	if !errors.Is(err, ErrTicketNotIssued) {
		t.Fatalf("second consume = %v, want ErrTicketNotIssued", err)
	}

	// A ticket that was never issued reports a different error.
	capture.queueResults([]storeRow{})
	err = store.Consume("t-unknown", time.Unix(1700000200, 0).UTC())
	if !errors.Is(err, ErrTicketNotFound) {
		t.Fatalf("unknown ticket consume = %v, want ErrTicketNotFound", err)
	}
}

func TestSQLBattleTicketStoreLoadDecodesRow(t *testing.T) {
	capture, name := registerStoreCaptureDriver(t)
	capture.queueResults([]storeRow{{
		columns: ticketRowColumns(),
		values:  ticketRowValues(TicketStatusIssued, nil),
	}})
	store, _ := NewSQLBattleTicketStore(openCaptureDB(t, name))
	ticket, ok, err := store.Load("t-1")
	if err != nil || !ok {
		t.Fatalf("load: ok=%v err=%v", ok, err)
	}
	if ticket.TicketID != "t-1" || ticket.Status != TicketStatusIssued || ticket.NonceHex != "cafe01" {
		t.Fatalf("ticket = %+v", ticket)
	}
	if !ticket.ConsumedAt.IsZero() {
		t.Fatalf("consumed at = %v", ticket.ConsumedAt)
	}
}

func TestSQLBattleTicketStoreExpireAndReject(t *testing.T) {
	capture, name := registerStoreCaptureDriver(t)
	store, _ := NewSQLBattleTicketStore(openCaptureDB(t, name))
	cutoff := time.Unix(1700000600, 0).UTC()
	if _, err := store.Expire(cutoff, 50); err != nil {
		t.Fatalf("expire: %v", err)
	}
	if err := store.Reject("t-1", "expired_signature", cutoff); err != nil {
		t.Fatalf("reject: %v", err)
	}
	calls := capture.execCalls()
	if len(calls) != 2 {
		t.Fatalf("expected two execs, got %d", len(calls))
	}
	if !strings.Contains(calls[0].query, "status = 'issued' AND expires_at < $1") {
		t.Fatalf("expire query = %s", calls[0].query)
	}
	if calls[0].args[1] != TicketStatusExpired || calls[0].args[2] != 50 {
		t.Fatalf("expire args = %v", calls[0].args)
	}
	if calls[1].args[1] != TicketStatusRejected || calls[1].args[2] != "expired_signature" {
		t.Fatalf("reject args = %v", calls[1].args)
	}
}

func TestBattleTicketMigrationCoversStoreColumns(t *testing.T) {
	upSQL := readMigrationFile(t, "006_battle_tickets.up.sql")
	downSQL := readMigrationFile(t, "006_battle_tickets.down.sql")
	assertMigrationCoversInsertColumns(t, upSQL, insertBattleTicketSQL, "battle_tickets")
	if !strings.Contains(upSQL, "uq_battle_tickets_active_player") {
		t.Fatalf("ticket migration must prevent a second live ticket per player")
	}
	if !strings.Contains(upSQL, "status IN ('issued', 'consumed', 'expired', 'revoked', 'rejected')") {
		t.Fatalf("ticket migration must constrain the status values the store writes")
	}
	assertMigrationHasColumns(t, upSQL, "idempotency_keys", []string{"scope", "idempotency_key", "request_fingerprint", "response_json", "status", "expires_at", "completed_at"})
	assertDownMigrationDropsTable(t, downSQL, "battle_tickets")
	assertDownMigrationDropsTable(t, downSQL, "idempotency_keys")
}

func TestMemoryBattleTicketStoreConsumesExactlyOnce(t *testing.T) {
	store := NewMemoryBattleTicketStore()
	if err := store.Issue(BattleTicketRecord{TicketID: "t-1", MatchID: "m-1", UserID: "u-1", ExpiresAt: time.Now().UTC().Add(time.Minute)}); err != nil {
		t.Fatalf("issue: %v", err)
	}
	if err := store.Consume("t-1", time.Now().UTC()); err != nil {
		t.Fatalf("first consume: %v", err)
	}
	if err := store.Consume("t-1", time.Now().UTC()); !errors.Is(err, ErrTicketNotIssued) {
		t.Fatalf("second consume = %v, want ErrTicketNotIssued", err)
	}
	if err := store.Consume("t-missing", time.Now().UTC()); !errors.Is(err, ErrTicketNotFound) {
		t.Fatalf("missing consume = %v, want ErrTicketNotFound", err)
	}
	tickets, err := store.ListByMatch("m-1")
	if err != nil || len(tickets) != 1 || tickets[0].Status != TicketStatusConsumed {
		t.Fatalf("tickets = %+v, %v", tickets, err)
	}
}

func TestMemoryBattleTicketStoreExpireOnlyTouchesIssuedTickets(t *testing.T) {
	store := NewMemoryBattleTicketStore()
	now := time.Now().UTC()
	if err := store.Issue(BattleTicketRecord{TicketID: "t-old", MatchID: "m-1", ExpiresAt: now.Add(-time.Minute)}); err != nil {
		t.Fatalf("issue: %v", err)
	}
	if err := store.Issue(BattleTicketRecord{TicketID: "t-new", MatchID: "m-1", ExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatalf("issue: %v", err)
	}
	expired, err := store.Expire(now, 10)
	if err != nil || expired != 1 {
		t.Fatalf("expire = %d, %v want 1", expired, err)
	}
	ticket, _, err := store.Load("t-old")
	if err != nil || ticket.Status != TicketStatusExpired {
		t.Fatalf("t-old = %+v, %v", ticket, err)
	}
}

func TestIdempotencyStoreClaimsOnce(t *testing.T) {
	store := NewMemoryIdempotencyStore()
	record := IdempotencyRecord{Scope: "chest_open", Key: "abc", RequestFingerprint: "sha256:req"}
	claimed, err := store.Claim(record)
	if err != nil || !claimed {
		t.Fatalf("first claim = %v, %v", claimed, err)
	}
	claimed, err = store.Claim(record)
	if err != nil || claimed {
		t.Fatalf("second claim = %v, %v", claimed, err)
	}
	if err := store.Complete("chest_open", "abc", `{"ok":true}`, time.Now().UTC()); err != nil {
		t.Fatalf("complete: %v", err)
	}
	loaded, ok, err := store.Load("chest_open", "abc")
	if err != nil || !ok || loaded.Status != IdempotencyCompleted || loaded.Response != `{"ok":true}` {
		t.Fatalf("loaded = %+v %v %v", loaded, ok, err)
	}
	if err := store.Complete("chest_open", "never-claimed", "{}", time.Now().UTC()); err == nil {
		t.Fatalf("completing an unclaimed key must fail")
	}
}

func TestSQLIdempotencyStoreClaimReportsOwnership(t *testing.T) {
	capture, name := registerStoreCaptureDriver(t)
	store, err := NewSQLIdempotencyStore(openCaptureDB(t, name))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	if _, err := store.Claim(IdempotencyRecord{Scope: "shop_buy", Key: "k1"}); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if !strings.Contains(capture.execCalls()[0].query, "ON CONFLICT (scope, idempotency_key) DO NOTHING") {
		t.Fatalf("claim query = %s", capture.execCalls()[0].query)
	}
	// Zero rows affected means another caller already owns the key.
	capture.setAffected(0)
	claimed, err := store.Claim(IdempotencyRecord{Scope: "shop_buy", Key: "k1"})
	if err != nil || claimed {
		t.Fatalf("second claim = %v, %v", claimed, err)
	}
}

func TestSQLIdempotencyStoreRejectsNilDatabase(t *testing.T) {
	if _, err := NewSQLIdempotencyStore(nil); err == nil {
		t.Fatalf("expected nil db rejection")
	}
}

func TestNewBattleTicketAndIdempotencyStoresSelectImplementation(t *testing.T) {
	tickets, err := NewBattleTicketStore(nil)
	if err != nil {
		t.Fatalf("new ticket store: %v", err)
	}
	if _, ok := tickets.(*MemoryBattleTicketStore); !ok {
		t.Fatalf("expected the memory ticket store, got %T", tickets)
	}
	idem, err := NewIdempotencyStore(nil)
	if err != nil {
		t.Fatalf("new idempotency store: %v", err)
	}
	if _, ok := idem.(*MemoryIdempotencyStore); !ok {
		t.Fatalf("expected the memory idempotency store, got %T", idem)
	}
}

func TestTicketAndIdempotencyImplementationsSatisfyTheInterfaces(t *testing.T) {
	var _ BattleTicketStore = (*SQLBattleTicketStore)(nil)
	var _ BattleTicketStore = (*MemoryBattleTicketStore)(nil)
	var _ IdempotencyStore = (*SQLIdempotencyStore)(nil)
	var _ IdempotencyStore = (*MemoryIdempotencyStore)(nil)
}
