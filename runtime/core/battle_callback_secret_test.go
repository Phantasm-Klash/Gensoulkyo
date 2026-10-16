package core

import (
	"testing"
)

// T6 fix: the per-battle-server callback secret is derived deterministically,
// so re-registering the same server id returns the same secret (no rotation,
// no state window between registration and the first callback).
func TestBattleServerCallbackSecretIsStableAcrossRegistrations(t *testing.T) {
	service := NewService(Config{CallbackSecret: "test-callback-master"})
	req := RegisterBattleServerRequest{
		BattleServerID: "stable-battle-1",
		Endpoint:       "127.0.0.1:7999",
		Capacity:       4,
		SupportedModes: []string{"pvp_duel"},
	}
	first, err := service.RegisterBattleServer(req)
	if err != nil {
		t.Fatalf("first registration: %v", err)
	}
	second, err := service.RegisterBattleServer(req)
	if err != nil {
		t.Fatalf("second registration: %v", err)
	}
	if first.CallbackSecret == "" {
		t.Fatalf("registration response must carry the callback secret")
	}
	if first.CallbackSecret != second.CallbackSecret {
		t.Fatalf("callback secret must be stable across registrations: %q vs %q", first.CallbackSecret, second.CallbackSecret)
	}
	if got := service.BattleServerCallbackSecret("stable-battle-1"); got != first.CallbackSecret {
		t.Fatalf("derived secret mismatch: %q vs %q", got, first.CallbackSecret)
	}

	// The secret must never leak through heartbeat, offline or the public list.
	heartbeat, err := service.BattleServerHeartbeat(BattleServerHeartbeatRequest{BattleServerID: "stable-battle-1", Endpoint: "127.0.0.1:7999"})
	if err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	if heartbeat.CallbackSecret != "" {
		t.Fatalf("heartbeat response must omit the callback secret, got %q", heartbeat.CallbackSecret)
	}
	offline, err := service.BattleServerOffline(BattleServerOfflineRequest{BattleServerID: "stable-battle-1"})
	if err != nil {
		t.Fatalf("offline: %v", err)
	}
	if offline.CallbackSecret != "" {
		t.Fatalf("offline response must omit the callback secret, got %q", offline.CallbackSecret)
	}
	for _, listed := range service.BattleServers().Servers {
		if listed.CallbackSecret != "" {
			t.Fatalf("public server list must omit the callback secret, got %q for %q", listed.CallbackSecret, listed.BattleServerID)
		}
	}
}

// T6 fix: signature verification accepts the exact HMAC of the raw body under
// the claimed server's derived secret and rejects everything else in constant
// time.
func TestBattleCallbackSignatureValid(t *testing.T) {
	service := NewService(Config{CallbackSecret: "test-callback-master"})
	body := []byte(`{"battle_server_id":"server-a","match_id":"m","events":[]}`)
	good := BattleCallbackSignature(service.BattleServerCallbackSecret("server-a"), body)

	if !service.BattleCallbackSignatureValid("server-a", body, good) {
		t.Fatalf("valid signature must be accepted")
	}
	if service.BattleCallbackSignatureValid("server-a", body, "") {
		t.Fatalf("missing signature must be rejected")
	}
	if service.BattleCallbackSignatureValid("server-a", body, "not-a-signature") {
		t.Fatalf("garbage signature must be rejected")
	}
	if service.BattleCallbackSignatureValid("server-b", body, good) {
		t.Fatalf("signature of another battle server must be rejected")
	}
	tampered := append([]byte(nil), body...)
	tampered[len(tampered)-2] = ' '
	if service.BattleCallbackSignatureValid("server-a", tampered, good) {
		t.Fatalf("tampered body must be rejected")
	}
	if service.BattleCallbackSignatureValid("", body, good) {
		t.Fatalf("empty battle server id must be rejected")
	}

	// A service built without an explicit secret still derives stable,
	// verifiable signatures (random per-process master).
	random := NewService(Config{})
	randomSecret := random.BattleServerCallbackSecret("server-a")
	if randomSecret == service.BattleServerCallbackSecret("server-a") {
		t.Fatalf("random master secrets must differ")
	}
	if !random.BattleCallbackSignatureValid("server-a", body, BattleCallbackSignature(randomSecret, body)) {
		t.Fatalf("random-master service must verify its own signatures")
	}
	if len(randomSecret) != 64 {
		t.Fatalf("derived secret must be 64 hex chars, got %q", randomSecret)
	}
}

// B4-7.3 fix: the master callback secret comes from configuration, so two
// consecutive NewService runs with the same configuration derive the SAME
// per-battle-server secret. This is what stops a lobby restart from silently
// rotating every battle server's callback secret.
func TestBattleServerCallbackSecretIsStableAcrossServiceRuns(t *testing.T) {
	const master = "configured-callback-master-0123456789abcdef"
	const serverID = "stable-battle-1"

	first := NewService(Config{CallbackSecret: master})
	second := NewService(Config{CallbackSecret: master})
	if got, want := first.BattleServerCallbackSecret(serverID), second.BattleServerCallbackSecret(serverID); got != want {
		t.Fatalf("two NewService runs with the same config must derive the same secret: %q vs %q", got, want)
	}

	// The configured master must actually feed the derivation: a different master
	// derives a different per-server secret.
	if first.BattleServerCallbackSecret(serverID) == NewService(Config{CallbackSecret: master + "-other"}).BattleServerCallbackSecret(serverID) {
		t.Fatalf("different masters must derive different per-server secrets")
	}
}
