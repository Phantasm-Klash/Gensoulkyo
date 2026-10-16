package core

import (
	"fmt"
	"testing"
	"time"
)

// B4-7.1: the signed material is byte-exact: timestamp + "\n" + nonce + "\n" +
// rawBody when both headers are present, and the raw body alone otherwise.
func TestBattleCallbackSignedMaterialExact(t *testing.T) {
	body := []byte(`{"a":1}`)
	if got := string(BattleCallbackSignedMaterial("1700000000", "abc", body)); got != "1700000000\nabc\n"+string(body) {
		t.Fatalf("fresh material mismatch: %q", got)
	}
	if got := BattleCallbackSignedMaterial("", "", body); string(got) != string(body) {
		t.Fatalf("legacy material must be the raw body, got %q", got)
	}
	// A partial header set falls back to the raw body here; VerifyBattleCallback
	// is what rejects it (never silently downgrading to the replayable form).
	if got := BattleCallbackSignedMaterial("1700000000", "", body); string(got) != string(body) {
		t.Fatalf("partial material must be the raw body, got %q", got)
	}
}

// B4-7.1: VerifyBattleCallback enforces freshness, uniqueness and the legacy
// path, all with stable error codes.
func TestVerifyBattleCallbackFreshness(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	service := NewService(Config{Clock: func() time.Time { return now }})
	const serverID = "fresh-core-1"
	secret := service.BattleServerCallbackSecret(serverID)
	body := []byte(`{"battle_server_id":"fresh-core-1"}`)

	fresh := func(ts string, nonce string) (string, bool) {
		material := BattleCallbackSignedMaterial(ts, nonce, body)
		return service.VerifyBattleCallback(serverID, body, ts, nonce, BattleCallbackSignature(secret, material))
	}

	tsNow := fmt.Sprintf("%d", now.Unix())
	if code, ok := fresh(tsNow, "n1"); !ok {
		t.Fatalf("fresh callback must pass, got %s", code)
	}
	if code, ok := fresh(tsNow, "n1"); ok || code != "battle_callback_replay" {
		t.Fatalf("replayed nonce must be rejected, got ok=%v code=%s", ok, code)
	}
	if code, ok := fresh(fmt.Sprintf("%d", now.Add(-301*time.Second).Unix()), "n2"); ok || code != "battle_callback_timestamp_stale" {
		t.Fatalf("stale timestamp must be rejected, got ok=%v code=%s", ok, code)
	}
	if code, ok := fresh(fmt.Sprintf("%d", now.Add(301*time.Second).Unix()), "n3"); ok || code != "battle_callback_timestamp_stale" {
		t.Fatalf("future timestamp must be rejected, got ok=%v code=%s", ok, code)
	}
	if code, ok := fresh(fmt.Sprintf("%d", now.Add(299*time.Second).Unix()), "n4"); !ok {
		t.Fatalf("in-window timestamp must pass, got %s", code)
	}
	if code, ok := fresh("not-a-number", "n5"); ok || code != "battle_callback_timestamp_stale" {
		t.Fatalf("unparseable timestamp must be rejected, got ok=%v code=%s", ok, code)
	}

	// Tampered body: the signature no longer matches the material.
	material := BattleCallbackSignedMaterial(tsNow, "n6", body)
	sig := BattleCallbackSignature(secret, material)
	if code, ok := service.VerifyBattleCallback(serverID, append(body, ' '), tsNow, "n6", sig); ok || code != "battle_callback_signature_invalid" {
		t.Fatalf("tampered body must be rejected, got ok=%v code=%s", ok, code)
	}
	// Partial header set: refused, not downgraded.
	if code, ok := service.VerifyBattleCallback(serverID, body, tsNow, "", ""); ok || code != "battle_callback_freshness_incomplete" {
		t.Fatalf("partial headers must be rejected, got ok=%v code=%s", ok, code)
	}
	// Oversize nonce: refused before it can consume any budget.
	big := make([]byte, maxBattleCallbackNonceLen+1)
	for i := range big {
		big[i] = 'x'
	}
	if code, ok := service.VerifyBattleCallback(serverID, body, tsNow, string(big), ""); ok || code != "battle_callback_nonce_invalid" {
		t.Fatalf("oversize nonce must be rejected, got ok=%v code=%s", ok, code)
	}

	// Legacy form still passes and is counted (the staged-rollout signal).
	if service.LegacyBattleCallbackCount() != 0 {
		t.Fatalf("no legacy callback expected yet, got %d", service.LegacyBattleCallbackCount())
	}
	if code, ok := service.VerifyBattleCallback(serverID, body, "", "", BattleCallbackSignature(secret, body)); !ok {
		t.Fatalf("legacy callback must pass, got %s", code)
	}
	if service.LegacyBattleCallbackCount() != 1 {
		t.Fatalf("legacy counter must be 1, got %d", service.LegacyBattleCallbackCount())
	}
}

// B4-7.1: the seen-nonce set stays bounded under a flood (evict oldest) and a
// recent nonce still replays.
func TestBattleCallbackNonceSetStaysBounded(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	service := NewService(Config{Clock: func() time.Time { return now }})
	const serverID = "fresh-core-flood"
	secret := service.BattleServerCallbackSecret(serverID)
	body := []byte(`{}`)
	ts := fmt.Sprintf("%d", now.Unix())

	total := maxBattleCallbackNoncesPerServer + 1000
	for i := 0; i < total; i++ {
		nonce := fmt.Sprintf("nonce-%d", i)
		material := BattleCallbackSignedMaterial(ts, nonce, body)
		if code, ok := service.VerifyBattleCallback(serverID, body, ts, nonce, BattleCallbackSignature(secret, material)); !ok {
			t.Fatalf("nonce %d must be accepted, got %s", i, code)
		}
	}
	window := service.battleCallbackNonces[serverID]
	if window == nil {
		t.Fatal("expected a nonce window for the flooded server")
	}
	if len(window.order) > maxBattleCallbackNoncesPerServer {
		t.Fatalf("nonce set exceeded the bound: %d > %d", len(window.order), maxBattleCallbackNoncesPerServer)
	}
	if len(window.seen) != len(window.order) {
		t.Fatalf("seen/order desync: %d vs %d", len(window.seen), len(window.order))
	}
	last := fmt.Sprintf("nonce-%d", total-1)
	material := BattleCallbackSignedMaterial(ts, last, body)
	if code, ok := service.VerifyBattleCallback(serverID, body, ts, last, BattleCallbackSignature(secret, material)); ok || code != "battle_callback_replay" {
		t.Fatalf("recent nonce must replay-reject, got ok=%v code=%s", ok, code)
	}
}
