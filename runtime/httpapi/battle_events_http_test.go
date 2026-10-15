package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"gensoulkyo/runtime/core"
)

// H-1: POST /v1/battle/events follows the submit-result guard sequence.
func TestHTTPBattleEventsCallbackGuards(t *testing.T) {
	service := core.NewService(core.Config{})
	server := httptest.NewServer(New(service))
	defer server.Close()

	route := server.URL + "/v1/battle/events"
	callbackHeaders := map[string]string{
		headerServiceOrigin:  core.ServiceCallbackContext()[serviceOriginContextKey],
		headerBattleCallback: core.ServiceCallbackContext()[serviceCallbackContextKey],
	}

	// Unauthenticated (no service-callback headers) must be rejected.
	response := postRaw(t, route, "", map[string]any{})
	if response.Code != http.StatusForbidden || response.ErrorCode != "service_origin_required" {
		t.Fatalf("battle events without callback headers must be rejected, got %+v", response)
	}

	// Player session context must be rejected.
	player := postJSON[core.AuthSession](t, server.URL+"/v1/auth/anonymous", "", map[string]any{"device_id": "http-events-player", "display_name": "HTTP Events Player"})
	response = postRaw(t, route, player.SessionToken, map[string]any{})
	if response.Code != http.StatusForbidden || response.ErrorCode != "service_origin_required" {
		t.Fatalf("battle events with player session must be rejected, got %+v", response)
	}

	// Unknown match must be rejected with 404 — but only after the callback
	// signature gate passes, so the post carries a valid signature for the
	// claimed battle server.
	unknownMatchBody := map[string]any{
		"battle_server_id": "battle-local-1",
		"match_id":         "match-nope",
		"events":           []map[string]any{{"type": "card_accepted", "tick": 1, "user_id": "u", "card_id": "focus_lens"}},
	}
	signedUnknownHeaders := map[string]string{}
	for key, value := range callbackHeaders {
		signedUnknownHeaders[key] = value
	}
	signedUnknownHeaders[headerBattleSignature] = signedCallbackBody(t, service.BattleServerCallbackSecret("battle-local-1"), unknownMatchBody)
	response = postRawWithHeaders(t, route, "", unknownMatchBody, signedUnknownHeaders)
	if response.Code != http.StatusNotFound || response.ErrorCode != "not_found" {
		t.Fatalf("battle events for unknown match must be rejected, got %+v", response)
	}
}

// H-1: an authenticated post for a live match becomes visible through
// GET /v1/matches/{id}/events.
func TestHTTPBattleEventsCallbackAppendsAndMatchesAreVisible(t *testing.T) {
	service := core.NewService(core.Config{})
	server := httptest.NewServer(New(service))
	defer server.Close()

	alice := postJSON[core.AuthSession](t, server.URL+"/v1/auth/anonymous", "", map[string]any{"device_id": "http-events-alice", "display_name": "HTTP Events Alice"})
	bob := postJSON[core.AuthSession](t, server.URL+"/v1/auth/anonymous", "", map[string]any{"device_id": "http-events-bob", "display_name": "HTTP Events Bob"})
	if _, err := service.RegisterBattleServer(core.RegisterBattleServerRequest{
		BattleServerID: "events-battle-1",
		Endpoint:       "127.0.0.1:7931",
		Region:         "local",
		BuildID:        "events-test",
		Capacity:       4,
		Status:         "online",
		SupportedModes: []string{"pvp_duel"},
	}); err != nil {
		t.Fatalf("register battle server: %v", err)
	}
	queueAlice, err := service.JoinQueue(alice.SessionToken, core.JoinQueueRequest{
		ModeID:       "pvp_duel",
		ActiveDeckID: "http_events_alice_deck",
		DeckSnapshot: httpEventsValidDeck("http_events_alice_deck"),
	})
	if err != nil {
		t.Fatalf("join alice: %v", err)
	}
	queueBob, err := service.JoinQueue(bob.SessionToken, core.JoinQueueRequest{
		ModeID:       "pvp_duel",
		ActiveDeckID: "http_events_bob_deck",
		DeckSnapshot: httpEventsValidDeck("http_events_bob_deck"),
	})
	if err != nil {
		t.Fatalf("join bob: %v", err)
	}
	matchID := queueAlice.MatchID
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
	serverID := allocation.BattleServerID
	if serverID == "" {
		t.Fatalf("allocation must name the battle server")
	}

	eventsBody := map[string]any{
		"battle_server_id": serverID,
		"match_id":         matchID,
		"events": []map[string]any{
			{"type": "card_accepted", "tick": 5, "user_id": alice.UserID, "card_id": "focus_lens", "slot": 1},
			{"type": "card_effect_applied", "tick": 5, "user_id": alice.UserID, "card_id": "focus_lens", "value": 3},
		},
	}
	signedEventsHeaders := map[string]string{
		headerServiceOrigin:  core.ServiceCallbackContext()[serviceOriginContextKey],
		headerBattleCallback: "yes",
		headerBattleSignature: signedCallbackBody(t, service.BattleServerCallbackSecret(serverID), eventsBody),
	}
	response := postRawWithHeaders(t, server.URL+"/v1/battle/events", "", eventsBody, signedEventsHeaders)
	if response.Code != http.StatusOK {
		t.Fatalf("authenticated battle events post must succeed, got %+v", response)
	}

	stream := getJSON[core.EventStreamResponse](t, server.URL+"/v1/matches/"+matchID+"/events", alice.SessionToken)
	found := map[string]bool{}
	for _, event := range stream.Events {
		if event.CardID == "focus_lens" {
			found[event.Type] = true
		}
	}
	if !found["card_accepted"] || !found["card_effect_applied"] {
		t.Fatalf("posted card events must be visible through MatchEvents, got %+v", stream.Events)
	}
}

func httpEventsValidDeck(deckID string) core.DeckSnapshot {
	cardIDs := []string{
		"focus_lens", "focus_lens",
		"hitbox_charm", "hitbox_charm",
		"density_surge", "density_surge",
		"tempo_break", "tempo_break",
		"bomb_amplifier", "bomb_amplifier",
		"guard_seal", "guard_seal",
		"graze_engine", "graze_engine",
		"draw_sigil", "draw_sigil",
		"aim_baffle", "aim_baffle",
		"purge_charm", "purge_charm",
	}
	return core.DeckSnapshot{
		DeckID:         deckID,
		Name:           deckID,
		RulesetVersion: core.RulesetVersion,
		CardIDs:        cardIDs,
	}
}
