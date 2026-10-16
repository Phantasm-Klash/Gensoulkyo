package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"gensoulkyo/runtime/core"
)

// B4-7.1: sign the freshness material (timestamp + "\n" + nonce + "\n" +
// rawBody) and return the three headers the C++ signer will emit.
func freshCallbackHeaders(secret string, raw []byte, timestamp string, nonce string) map[string]string {
	headers := serviceCallbackHeaders()
	headers[headerBattleTimestamp] = timestamp
	headers[headerBattleNonce] = nonce
	headers[headerBattleSignature] = core.BattleCallbackSignature(secret, core.BattleCallbackSignedMaterial(timestamp, nonce, raw))
	return headers
}

// B4-7.1: a fresh signed callback passes; a verbatim replay of the same nonce is
// rejected with a stable code, and the fresh path is NOT counted as legacy.
func TestHTTPBattleEventsCallbackAcceptsFreshSignatureAndRejectsReplay(t *testing.T) {
	service := core.NewService(core.Config{})
	server := httptest.NewServer(New(service))
	defer server.Close()

	const serverID = "fresh-battle-1"
	matchID, userID := newRunningMatchWithBattleServer(t, service, server.URL, serverID)
	secret := service.BattleServerCallbackSecret(serverID)
	body := battleEventsCallbackBody(matchID, serverID, userID)
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	route := server.URL + "/v1/battle/events"
	ts := fmt.Sprintf("%d", time.Now().Unix())
	const nonce = "fresh-nonce-1"

	response := postRawBytesWithHeaders(t, route, "", raw, freshCallbackHeaders(secret, raw, ts, nonce))
	if response.Code != http.StatusOK {
		t.Fatalf("fresh signed callback must succeed, got %+v", response)
	}
	if service.LegacyBattleCallbackCount() != 0 {
		t.Fatalf("fresh callback must not be counted as legacy, got %d", service.LegacyBattleCallbackCount())
	}

	response = postRawBytesWithHeaders(t, route, "", raw, freshCallbackHeaders(secret, raw, ts, nonce))
	if response.Code != http.StatusForbidden || response.ErrorCode != "battle_callback_replay" {
		t.Fatalf("replayed nonce must be rejected, got %+v", response)
	}
}

// B4-7.1: a timestamp outside ±300s is rejected (both stale and future).
func TestHTTPBattleEventsCallbackRejectsStaleTimestamp(t *testing.T) {
	service := core.NewService(core.Config{})
	server := httptest.NewServer(New(service))
	defer server.Close()

	const serverID = "fresh-battle-2"
	matchID, userID := newRunningMatchWithBattleServer(t, service, server.URL, serverID)
	secret := service.BattleServerCallbackSecret(serverID)
	body := battleEventsCallbackBody(matchID, serverID, userID)
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	route := server.URL + "/v1/battle/events"

	staleTS := fmt.Sprintf("%d", time.Now().Add(-time.Hour).Unix())
	response := postRawBytesWithHeaders(t, route, "", raw, freshCallbackHeaders(secret, raw, staleTS, "stale-nonce"))
	if response.Code != http.StatusForbidden || response.ErrorCode != "battle_callback_timestamp_stale" {
		t.Fatalf("stale timestamp must be rejected, got %+v", response)
	}

	futureTS := fmt.Sprintf("%d", time.Now().Add(time.Hour).Unix())
	response = postRawBytesWithHeaders(t, route, "", raw, freshCallbackHeaders(secret, raw, futureTS, "future-nonce"))
	if response.Code != http.StatusForbidden || response.ErrorCode != "battle_callback_timestamp_stale" {
		t.Fatalf("future timestamp must be rejected, got %+v", response)
	}
}

// B4-7.1: tampering a byte of a freshness-signed body invalidates the signature.
func TestHTTPBattleEventsCallbackRejectsTamperedFreshBody(t *testing.T) {
	service := core.NewService(core.Config{})
	server := httptest.NewServer(New(service))
	defer server.Close()

	const serverID = "fresh-battle-3"
	matchID, userID := newRunningMatchWithBattleServer(t, service, server.URL, serverID)
	secret := service.BattleServerCallbackSecret(serverID)
	body := battleEventsCallbackBody(matchID, serverID, userID)
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	ts := fmt.Sprintf("%d", time.Now().Unix())
	const nonce = "tamper-nonce"
	headers := freshCallbackHeaders(secret, raw, ts, nonce)

	tampered := append([]byte(nil), raw...)
	at := bytes.Index(tampered, []byte("focus_lens"))
	if at < 0 {
		t.Fatalf("test body must contain the card id")
	}
	tampered[at] = 'g'

	response := postRawBytesWithHeaders(t, server.URL+"/v1/battle/events", "", tampered, headers)
	if response.Code != http.StatusForbidden || response.ErrorCode != "battle_callback_signature_invalid" {
		t.Fatalf("tampered fresh body must be rejected, got %+v", response)
	}
}

// B4-7.1: the legacy (no freshness headers) form still passes and is counted, so
// the rollout is observable and nothing is silently weakened.
func TestHTTPBattleEventsCallbackLegacyFormStillPassesAndIsCounted(t *testing.T) {
	service := core.NewService(core.Config{})
	server := httptest.NewServer(New(service))
	defer server.Close()

	const serverID = "fresh-battle-4"
	matchID, userID := newRunningMatchWithBattleServer(t, service, server.URL, serverID)
	secret := service.BattleServerCallbackSecret(serverID)
	body := battleEventsCallbackBody(matchID, serverID, userID)

	headers := serviceCallbackHeaders()
	headers[headerBattleSignature] = signedCallbackBody(t, secret, body)
	response := postRawWithHeaders(t, server.URL+"/v1/battle/events", "", body, headers)
	if response.Code != http.StatusOK {
		t.Fatalf("legacy signed callback must still succeed, got %+v", response)
	}
	if service.LegacyBattleCallbackCount() != 1 {
		t.Fatalf("legacy callback must be counted, got %d", service.LegacyBattleCallbackCount())
	}
}

// B4-7.1: supplying only one of the two freshness headers is refused rather than
// downgraded to the replayable legacy material.
func TestHTTPBattleEventsCallbackRejectsPartialFreshnessHeaders(t *testing.T) {
	service := core.NewService(core.Config{})
	server := httptest.NewServer(New(service))
	defer server.Close()

	const serverID = "fresh-battle-5"
	matchID, userID := newRunningMatchWithBattleServer(t, service, server.URL, serverID)
	secret := service.BattleServerCallbackSecret(serverID)
	body := battleEventsCallbackBody(matchID, serverID, userID)

	headers := serviceCallbackHeaders()
	headers[headerBattleTimestamp] = fmt.Sprintf("%d", time.Now().Unix())
	headers[headerBattleSignature] = signedCallbackBody(t, secret, body)
	response := postRawWithHeaders(t, server.URL+"/v1/battle/events", "", body, headers)
	if response.Code != http.StatusForbidden || response.ErrorCode != "battle_callback_freshness_incomplete" {
		t.Fatalf("partial freshness headers must be rejected, got %+v", response)
	}
}
