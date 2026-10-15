package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"gensoulkyo/runtime/core"
)

// signedCallbackBody marshals a callback body exactly like the battle server
// would (Go encoding/json is deterministic for maps and structs) and returns
// the X-PhK-Battle-Signature header value for those exact bytes.
func signedCallbackBody(t *testing.T, secret string, body any) string {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal callback body: %v", err)
	}
	return core.BattleCallbackSignature(secret, raw)
}

// postRawBytesWithHeaders posts exact raw bytes (used for tampered-body tests
// where re-marshalling would undo the tamper).
func postRawBytesWithHeaders(t *testing.T, url string, token string, raw []byte, headers map[string]string) errorBody {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out errorBody
	_ = json.NewDecoder(resp.Body).Decode(&out)
	out.Code = resp.StatusCode
	return out
}

// newRunningMatchWithBattleServer brings up a running pvp_duel match whose
// allocation is owned by the freshly registered battleServerID.
func newRunningMatchWithBattleServer(t *testing.T, service *core.Service, serverURL string, battleServerID string) (matchID string, playerUserID string) {
	t.Helper()
	alice := postJSON[core.AuthSession](t, serverURL+"/v1/auth/anonymous", "", map[string]any{"device_id": "sig-alice-" + battleServerID, "display_name": "Sig Alice"})
	bob := postJSON[core.AuthSession](t, serverURL+"/v1/auth/anonymous", "", map[string]any{"device_id": "sig-bob-" + battleServerID, "display_name": "Sig Bob"})
	if _, err := service.RegisterBattleServer(core.RegisterBattleServerRequest{
		BattleServerID: battleServerID,
		Endpoint:       "127.0.0.1:7921",
		Region:         "local",
		Capacity:       4,
		Status:         "online",
		SupportedModes: []string{"pvp_duel"},
	}); err != nil {
		t.Fatalf("register battle server: %v", err)
	}
	queueAlice, err := service.JoinQueue(alice.SessionToken, core.JoinQueueRequest{
		ModeID:       "pvp_duel",
		ActiveDeckID: "sig_alice_deck_" + battleServerID,
		DeckSnapshot: httpEventsValidDeck("sig_alice_deck_" + battleServerID),
	})
	if err != nil {
		t.Fatalf("join alice: %v", err)
	}
	queueBob, err := service.JoinQueue(bob.SessionToken, core.JoinQueueRequest{
		ModeID:       "pvp_duel",
		ActiveDeckID: "sig_bob_deck_" + battleServerID,
		DeckSnapshot: httpEventsValidDeck("sig_bob_deck_" + battleServerID),
	})
	if err != nil {
		t.Fatalf("join bob: %v", err)
	}
	matchID = queueAlice.MatchID
	if matchID == "" {
		matchID = queueBob.MatchID
	}
	if matchID == "" {
		t.Fatalf("expected match after queue fill")
	}
	if _, err := service.ReadyMatch(alice.SessionToken, matchID); err != nil {
		t.Fatalf("ready alice: %v", err)
	}
	if _, err := service.ReadyMatch(bob.SessionToken, matchID); err != nil {
		t.Fatalf("ready bob: %v", err)
	}
	allocation, err := service.BattleAllocation(alice.SessionToken, matchID)
	if err != nil {
		t.Fatalf("allocation: %v", err)
	}
	if allocation.BattleServerID != battleServerID {
		t.Fatalf("match must be allocated to %q, got %q", battleServerID, allocation.BattleServerID)
	}
	return matchID, alice.UserID
}

func battleEventsCallbackBody(matchID string, battleServerID string, userID string) map[string]any {
	return map[string]any{
		"battle_server_id": battleServerID,
		"match_id":         matchID,
		"events": []map[string]any{
			{"type": "card_accepted", "tick": 3, "user_id": userID, "card_id": "focus_lens", "slot": 1},
		},
	}
}

// T6 fix: POST /v1/battle/events requires the HMAC callback signature. A
// missing or wrong X-PhK-Battle-Signature must be rejected with 403 before any
// business processing.
func TestHTTPBattleEventsCallbackRequiresValidSignature(t *testing.T) {
	service := core.NewService(core.Config{})
	server := httptest.NewServer(New(service))
	defer server.Close()

	const serverID = "sig-battle-1"
	matchID, userID := newRunningMatchWithBattleServer(t, service, server.URL, serverID)
	secret := service.BattleServerCallbackSecret(serverID)
	body := battleEventsCallbackBody(matchID, serverID, userID)
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	route := server.URL + "/v1/battle/events"

	// Missing signature: public headers alone no longer reach the business layer.
	response := postRawBytesWithHeaders(t, route, "", raw, serviceCallbackHeaders())
	if response.Code != http.StatusForbidden || response.ErrorCode != "battle_callback_signature_invalid" {
		t.Fatalf("battle events without signature must be rejected, got %+v", response)
	}

	// Wrong signature (computed over a different body).
	wrongHeaders := serviceCallbackHeaders()
	wrongHeaders[headerBattleSignature] = signedCallbackBody(t, secret, map[string]any{"tampered": true})
	response = postRawBytesWithHeaders(t, route, "", raw, wrongHeaders)
	if response.Code != http.StatusForbidden || response.ErrorCode != "battle_callback_signature_invalid" {
		t.Fatalf("battle events with wrong signature must be rejected, got %+v", response)
	}

	// Signature under another battle server's derived secret.
	foreignHeaders := serviceCallbackHeaders()
	foreignHeaders[headerBattleSignature] = signedCallbackBody(t, service.BattleServerCallbackSecret("sig-battle-other"), body)
	response = postRawWithHeaders(t, route, "", body, foreignHeaders)
	if response.Code != http.StatusForbidden || response.ErrorCode != "battle_callback_signature_invalid" {
		t.Fatalf("battle events signed by a foreign server must be rejected, got %+v", response)
	}
}

// T6 fix: a valid signature over the exact raw body is accepted by the guard
// and processed by the business layer (200 with rows appended; empty events
// reach the business layer and are rejected there, not at the signature gate).
func TestHTTPBattleEventsCallbackAcceptsValidSignature(t *testing.T) {
	service := core.NewService(core.Config{})
	server := httptest.NewServer(New(service))
	defer server.Close()

	const serverID = "sig-battle-2"
	matchID, userID := newRunningMatchWithBattleServer(t, service, server.URL, serverID)
	secret := service.BattleServerCallbackSecret(serverID)
	route := server.URL + "/v1/battle/events"

	body := battleEventsCallbackBody(matchID, serverID, userID)
	headers := serviceCallbackHeaders()
	headers[headerBattleSignature] = signedCallbackBody(t, secret, body)
	response := postRawWithHeaders(t, route, "", body, headers)
	if response.Code != http.StatusOK {
		t.Fatalf("battle events with a valid signature must succeed, got %+v", response)
	}

	// Empty events with a valid signature: the guard passes, the business layer
	// rejects ("events are required") — proof the signature gate let it through.
	emptyBody := map[string]any{
		"battle_server_id": serverID,
		"match_id":         matchID,
		"events":           []map[string]any{},
	}
	emptyHeaders := serviceCallbackHeaders()
	emptyHeaders[headerBattleSignature] = signedCallbackBody(t, secret, emptyBody)
	response = postRawWithHeaders(t, route, "", emptyBody, emptyHeaders)
	if response.Code != http.StatusBadRequest || response.ErrorCode != "invalid_request" {
		t.Fatalf("signed empty events must reach the business layer, got %+v", response)
	}
}

// T6 fix: a valid signature from server A cannot write into a match owned by
// server B — the guard asserts match ownership against the authenticated
// battle server id.
func TestHTTPBattleEventsCallbackRejectsForeignServerOwnership(t *testing.T) {
	service := core.NewService(core.Config{})
	server := httptest.NewServer(New(service))
	defer server.Close()

	matchID, userID := newRunningMatchWithBattleServer(t, service, server.URL, "sig-owner")
	impostorID := "sig-impostor"
	body := battleEventsCallbackBody(matchID, impostorID, userID)
	headers := serviceCallbackHeaders()
	// A perfectly valid signature for the impostor server — the ownership
	// assertion must still reject the callback.
	headers[headerBattleSignature] = signedCallbackBody(t, service.BattleServerCallbackSecret(impostorID), body)
	response := postRawWithHeaders(t, server.URL+"/v1/battle/events", "", body, headers)
	if response.Code != http.StatusForbidden || response.ErrorCode != "battle_callback_server_mismatch" {
		t.Fatalf("foreign server signature must be rejected by the ownership assertion, got %+v", response)
	}
}

// T6 fix: flipping one byte of a signed body invalidates the signature.
func TestHTTPBattleEventsCallbackRejectsTamperedBody(t *testing.T) {
	service := core.NewService(core.Config{})
	server := httptest.NewServer(New(service))
	defer server.Close()

	const serverID = "sig-battle-3"
	matchID, userID := newRunningMatchWithBattleServer(t, service, server.URL, serverID)
	secret := service.BattleServerCallbackSecret(serverID)
	body := battleEventsCallbackBody(matchID, serverID, userID)
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	signature := core.BattleCallbackSignature(secret, raw)

	// Tamper inside the card id string: still valid JSON, different body bytes.
	tampered := append([]byte(nil), raw...)
	at := bytes.Index(tampered, []byte("focus_lens"))
	if at < 0 {
		t.Fatalf("test body must contain the card id")
	}
	tampered[at] = 'g'

	headers := serviceCallbackHeaders()
	headers[headerBattleSignature] = signature
	response := postRawBytesWithHeaders(t, server.URL+"/v1/battle/events", "", tampered, headers)
	if response.Code != http.StatusForbidden || response.ErrorCode != "battle_callback_signature_invalid" {
		t.Fatalf("tampered body must be rejected, got %+v", response)
	}
}

// T6 fix: the result callback (POST /v1/battle/results/submit) shares the same
// signature boundary.
func TestHTTPBattleResultSubmitRequiresValidSignature(t *testing.T) {
	service := core.NewService(core.Config{})
	server := httptest.NewServer(New(service))
	defer server.Close()

	const serverID = "sig-battle-4"
	body := core.BattleResultSubmitRequest{
		SignedResult: core.SignedBattleResult{
			KeyID: serverID,
			Result: core.BattleResult{
				MatchID: "match-sig-4",
			},
		},
	}
	route := server.URL + "/v1/battle/results/submit"

	response := postRawWithHeaders(t, route, "", body, serviceCallbackHeaders())
	if response.Code != http.StatusForbidden || response.ErrorCode != "battle_callback_signature_invalid" {
		t.Fatalf("result submit without signature must be rejected, got %+v", response)
	}

	wrongHeaders := serviceCallbackHeaders()
	wrongHeaders[headerBattleSignature] = signedCallbackBody(t, service.BattleServerCallbackSecret(serverID), map[string]any{"tampered": true})
	response = postRawWithHeaders(t, route, "", body, wrongHeaders)
	if response.Code != http.StatusForbidden || response.ErrorCode != "battle_callback_signature_invalid" {
		t.Fatalf("result submit with wrong signature must be rejected, got %+v", response)
	}
}
