package httpapi

import (
	"bytes"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"gensoulkyo/runtime/core"
	"gensoulkyo/runtime/security"
)

func TestHTTPAuthMatchInputAndSettlement(t *testing.T) {
	service := core.NewService(core.Config{})
	server := httptest.NewServer(New(service))
	defer server.Close()

	alice := postJSON[core.AuthSession](t, server.URL+"/v1/auth/anonymous", "", map[string]any{"device_id": "a", "display_name": "Alice"})
	bob := postJSON[core.AuthSession](t, server.URL+"/v1/auth/anonymous", "", map[string]any{"device_id": "b", "display_name": "Bob"})

	bootstrap := getJSON[core.BootstrapSnapshot](t, server.URL+"/v1/bootstrap", alice.SessionToken)
	if bootstrap.ServerVersion != core.ServerVersion || len(bootstrap.Modes) < 4 || len(bootstrap.Inventory.Items) == 0 || bootstrap.Decks.ActiveDeckID == "" {
		t.Fatalf("bootstrap invalid: %+v", bootstrap)
	}
	if !bootstrap.Certification.OK || bootstrap.Certification.RatingCode == "" || !bootstrap.Certification.ServerAuthoritative {
		t.Fatalf("bootstrap certification invalid: %+v", bootstrap.Certification)
	}
	if !bootstrap.Chests.OK || len(bootstrap.Chests.Pools) == 0 || bootstrap.Chests.OwnedChests["local_basic"] <= 0 || bootstrap.Wallet["chest_keys"] <= 0 {
		t.Fatalf("bootstrap chest projection invalid: %+v", bootstrap.Chests)
	}
	inventory := getJSON[core.InventorySnapshot](t, server.URL+"/v1/inventory", alice.SessionToken)
	if !inventory.OK || !inventory.ServerAuthoritative || len(inventory.Items) != len(bootstrap.Inventory.Items) {
		t.Fatalf("inventory invalid: %+v", inventory)
	}
	chests := getJSON[core.ChestSnapshot](t, server.URL+"/v1/chests", alice.SessionToken)
	if !chests.OK || !chests.ServerAuthoritative || len(chests.Pools) != len(bootstrap.Chests.Pools) {
		t.Fatalf("chests invalid: %+v", chests)
	}
	forbiddenChest := postRaw(t, server.URL+"/v1/chests/open", alice.SessionToken, map[string]any{
		"pool_id":                     "local_basic",
		"count":                       1,
		"client_result_authoritative": true,
	})
	if forbiddenChest.Code != http.StatusForbidden || forbiddenChest.ErrorCode != "forbidden_field" {
		t.Fatalf("expected forbidden chest authority, got %+v", forbiddenChest)
	}
	openChest := postJSON[core.ChestOpenResponse](t, server.URL+"/v1/chests/open", alice.SessionToken, map[string]any{
		"pool_id": "local_basic",
		"count":   1,
	})
	if !openChest.OK || !openChest.ServerAuthoritative || len(openChest.Results) != 1 || openChest.Audit.OpeningID == "" || openChest.OwnedChests["local_basic"] != 0 {
		t.Fatalf("open chest invalid: %+v", openChest)
	}
	forbiddenUpgrade := postRaw(t, server.URL+"/v1/cards/upgrade", alice.SessionToken, map[string]any{
		"card_id":                     openChest.Results[0].CardID,
		"client_result_authoritative": true,
	})
	if forbiddenUpgrade.Code != http.StatusForbidden || forbiddenUpgrade.ErrorCode != "forbidden_field" {
		t.Fatalf("expected forbidden card upgrade authority, got %+v", forbiddenUpgrade)
	}
	upgrade := postJSON[core.CardUpgradeResponse](t, server.URL+"/v1/cards/upgrade", alice.SessionToken, map[string]any{
		"card_id": openChest.Results[0].CardID,
	})
	if !upgrade.OK || !upgrade.ServerAuthoritative || upgrade.ClientResultAuthoritative || upgrade.NewLevel != 2 || upgrade.Cost["card_dust"] <= 0 {
		t.Fatalf("card upgrade invalid: %+v", upgrade)
	}
	savedIDs := []string{
		"draw_sigil", "draw_sigil",
		"graze_engine", "graze_engine",
		"purge_charm", "purge_charm",
		"curve_prism", "curve_prism",
		"tempo_break", "tempo_break",
		"hitbox_charm", "hitbox_charm",
		"bomb_amplifier", "bomb_amplifier",
		"focus_lens", "focus_lens",
		"guard_seal", "guard_seal",
		"aim_baffle", "aim_baffle",
	}
	save := postJSON[core.SaveDeckResponse](t, server.URL+"/v1/decks/save", alice.SessionToken, map[string]any{
		"deck_id":  "http_active",
		"name":     "HTTP Active",
		"format":   "local_practice",
		"card_ids": savedIDs,
		"active":   true,
	})
	if !save.OK || save.ActiveDeckID != "http_active" || !save.ServerAuthoritative {
		t.Fatalf("save deck invalid: %+v", save)
	}
	decks := getJSON[core.DeckListResponse](t, server.URL+"/v1/decks", alice.SessionToken)
	if !decks.OK || decks.ActiveDeckID != "http_active" || len(decks.Decks) < 2 {
		t.Fatalf("decks invalid: %+v", decks)
	}

	_ = postJSON[core.QueueResponse](t, server.URL+"/v1/matchmaking/join", alice.SessionToken, map[string]any{
		"mode_id":        "certification",
		"active_deck_id": "http_active",
		"deck_snapshot":  validDeck("alice_deck"),
		"mode_params":    map[string]any{"stage_id": "lunar_maze", "character_id": "spell_power"},
	})
	queueBob := postJSON[core.QueueResponse](t, server.URL+"/v1/matchmaking/join", bob.SessionToken, map[string]any{
		"mode_id":        "certification",
		"active_deck_id": "bob_deck",
		"deck_snapshot":  validDeck("bob_deck"),
		"mode_params":    map[string]any{"stage_id": "lunar_maze", "character_id": "precision"},
	})
	if queueBob.MatchID == "" || queueBob.Loadout.StageID != "lunar_maze" {
		t.Fatalf("expected match: %+v", queueBob)
	}
	postJSON[core.ReadyResponse](t, server.URL+"/v1/matches/"+queueBob.MatchID+"/ready", alice.SessionToken, map[string]any{})
	readyBob := postJSON[core.ReadyResponse](t, server.URL+"/v1/matches/"+queueBob.MatchID+"/ready", bob.SessionToken, map[string]any{})
	if readyBob.MatchStart == nil || readyBob.MatchStart.Type != "match_start" {
		t.Fatalf("missing match start: %+v", readyBob)
	}
	if readyBob.MatchStart.StageID != "lunar_maze" || len(readyBob.MatchStart.Players) != 2 || readyBob.MatchStart.Players[0].Loadout.StageID == "" {
		t.Fatalf("match start missing loadout: %+v", readyBob.MatchStart)
	}

	input := postJSON[core.InputResponse](t, server.URL+"/v1/matches/"+queueBob.MatchID+"/input", alice.SessionToken, map[string]any{
		"tick":      1,
		"seq":       1,
		"dir":       4,
		"slow":      true,
		"shoot":     true,
		"bomb":      false,
		"card_slot": -1,
	})
	if !input.Accepted || input.Snapshot.StateHash == "" {
		t.Fatalf("input invalid: %+v", input)
	}
	if input.Snapshot.StageID != "lunar_maze" || len(input.Snapshot.Players) == 0 || input.Snapshot.Players[0].Loadout.StageID == "" {
		t.Fatalf("input snapshot missing loadout: %+v", input.Snapshot)
	}
	cardInput := postJSON[core.InputResponse](t, server.URL+"/v1/matches/"+queueBob.MatchID+"/input", alice.SessionToken, map[string]any{
		"tick":      2,
		"seq":       2,
		"dir":       0,
		"slow":      false,
		"shoot":     false,
		"bomb":      false,
		"card_slot": 0,
	})
	if !cardInput.Accepted || len(cardInput.Snapshot.ActiveCards) != 1 || cardInput.Snapshot.ActiveCards[0].CardID != "draw_sigil" {
		t.Fatalf("card input did not return authoritative active card: %+v", cardInput)
	}
	events := getJSON[core.EventStreamResponse](t, server.URL+"/v1/matches/"+queueBob.MatchID+"/events?after=0&limit=4", alice.SessionToken)
	if !events.OK || events.MatchID != queueBob.MatchID || events.Cursor <= 0 || len(events.Events) == 0 {
		t.Fatalf("event stream invalid: %+v", events)
	}
	if !hasEventType(events.Events, "player_ready") && !events.HasMore {
		t.Fatalf("event stream should include ready events or report more data: %+v", events)
	}
	tail := getJSON[core.EventStreamResponse](t, server.URL+"/v1/matches/"+queueBob.MatchID+"/events?after="+itoa(events.Cursor), alice.SessionToken)
	if tail.Cursor < events.Cursor {
		t.Fatalf("event stream cursor moved backward: first=%+v tail=%+v", events, tail)
	}
	disconnect := postJSON[core.ReconnectResponse](t, server.URL+"/v1/matches/"+queueBob.MatchID+"/disconnect", alice.SessionToken, map[string]any{})
	if disconnect.ReconnectStatus != "disconnected" || disconnect.Connected || !disconnect.Snapshot.Full {
		t.Fatalf("disconnect response invalid: %+v", disconnect)
	}
	disconnectedInput := postRaw(t, server.URL+"/v1/matches/"+queueBob.MatchID+"/input", alice.SessionToken, map[string]any{
		"tick":      3,
		"seq":       3,
		"dir":       0,
		"card_slot": -1,
	})
	if disconnectedInput.Code != http.StatusConflict || disconnectedInput.ErrorCode != "match_state_invalid" {
		t.Fatalf("expected disconnected input conflict, got %+v", disconnectedInput)
	}
	reconnect := postJSON[core.ReconnectResponse](t, server.URL+"/v1/matches/"+queueBob.MatchID+"/reconnect", alice.SessionToken, map[string]any{})
	if reconnect.ReconnectStatus != "restored" || !reconnect.Connected || reconnect.MatchStart == nil || !reconnect.Snapshot.Full {
		t.Fatalf("reconnect response invalid: %+v", reconnect)
	}

	forbidden := postRaw(t, server.URL+"/v1/matches/"+queueBob.MatchID+"/input", alice.SessionToken, map[string]any{
		"tick":        3,
		"seq":         3,
		"dir":         0,
		"reward_json": []any{},
	})
	if forbidden.Code != http.StatusForbidden || forbidden.ErrorCode != "forbidden_field" {
		t.Fatalf("expected forbidden field rejection, got %+v", forbidden)
	}

	settlement := postJSON[core.MatchEndEvent](t, server.URL+"/v1/matches/"+queueBob.MatchID+"/settle", alice.SessionToken, map[string]any{})
	if settlement.Type != "match_end" || !settlement.ServerAuthoritative || settlement.ReplayID == "" {
		t.Fatalf("settlement invalid: %+v", settlement)
	}
	if settlement.StageID != "lunar_maze" || settlement.Loadout.CharacterID == "" {
		t.Fatalf("settlement missing loadout: %+v", settlement)
	}
	if settlement.Loadout.RatingCode == "" || settlement.ModeResult["rank_score_delta"] == nil || settlement.ModeResult["next_certification_unlocked"] == nil {
		t.Fatalf("settlement missing certification result: loadout=%+v mode=%+v", settlement.Loadout, settlement.ModeResult)
	}
	settlementEvent := postJSON[core.BusinessEvent](t, server.URL+"/v1/business/events", alice.SessionToken, map[string]any{
		"kind":     "settlement",
		"match_id": queueBob.MatchID,
	})
	if settlementEvent.Topic != "nakama_wss.business.settlement" || settlementEvent.Settlement == nil || settlementEvent.Settlement.ReplayID != settlement.ReplayID || settlementEvent.ClientResultSubmitAllowed || settlementEvent.HighFrequencyBattleTickAllowed {
		t.Fatalf("settlement business event invalid: %+v", settlementEvent)
	}
	if !settlementEvent.BusinessEnvelopeRequired || !stringSliceContains(settlementEvent.ForbiddenFields, "final_result") || !stringSliceContains(settlementEvent.ForbiddenFields, "settlement_key") {
		t.Fatalf("settlement business event should expose security contract: %+v", settlementEvent)
	}
	if settlementEvent.Version.ProtocolVersion != core.ProtocolVersion || settlementEvent.Version.RulesetVersion != core.RulesetVersion || settlementEvent.Version.BusinessAPIVersion != core.BusinessAPIVersion || settlementEvent.Version.BattleAPIVersion != core.BattleAPIVersion {
		t.Fatalf("settlement business event missing version stamp: %+v", settlementEvent.Version)
	}
	forbiddenSettlementEvent := postRaw(t, server.URL+"/v1/business/events", alice.SessionToken, map[string]any{
		"kind":        "settlement",
		"match_id":    queueBob.MatchID,
		"result_hash": "client-authored",
	})
	if forbiddenSettlementEvent.Code != http.StatusForbidden || forbiddenSettlementEvent.ErrorCode != "forbidden_field" {
		t.Fatalf("expected forbidden business event settlement authority, got %+v", forbiddenSettlementEvent)
	}
	extraSettlementEvent := postRaw(t, server.URL+"/v1/business/events", alice.SessionToken, map[string]any{
		"kind":        "settlement",
		"match_id":    queueBob.MatchID,
		"client_note": "not part of lookup contract",
	})
	if extraSettlementEvent.Code != http.StatusBadRequest || extraSettlementEvent.ErrorCode != "invalid_request" || !strings.Contains(extraSettlementEvent.Message, "client_note") {
		t.Fatalf("expected non-lookup business event field rejection, got %+v", extraSettlementEvent)
	}
	replay := getJSON[core.ReplayRecord](t, server.URL+"/v1/replays/"+settlement.ReplayID, alice.SessionToken)
	if !replay.OK || replay.ReplayID != settlement.ReplayID || replay.MatchID != queueBob.MatchID || !replay.ServerAuthoritative || replay.StateHash == "" {
		t.Fatalf("replay invalid: %+v", replay)
	}
	if replay.StageID != "lunar_maze" || replay.Loadout.StageID != settlement.Loadout.StageID {
		t.Fatalf("replay missing loadout: %+v", replay)
	}
	if replay.InputCount == 0 || replay.EventCount == 0 || replay.Settlement.ReplayID != settlement.ReplayID {
		t.Fatalf("replay missing audit payload: %+v", replay)
	}
	intruder := postJSON[core.AuthSession](t, server.URL+"/v1/auth/anonymous", "", map[string]any{"device_id": "intruder", "display_name": "Intruder"})
	forbiddenReplay := getRaw(t, server.URL+"/v1/replays/"+settlement.ReplayID, intruder.SessionToken)
	if forbiddenReplay.Code != http.StatusUnauthorized || forbiddenReplay.ErrorCode != "unauthorized" {
		t.Fatalf("expected cross-user replay rejection, got %+v", forbiddenReplay)
	}
	forbiddenRematch := postRaw(t, server.URL+"/v1/matches/"+queueBob.MatchID+"/rematch", intruder.SessionToken, map[string]any{})
	if forbiddenRematch.Code != http.StatusUnauthorized || forbiddenRematch.ErrorCode != "unauthorized" {
		t.Fatalf("expected cross-user rematch rejection, got %+v", forbiddenRematch)
	}
	rematchAlice := postJSON[core.RematchResponse](t, server.URL+"/v1/matches/"+queueBob.MatchID+"/rematch", alice.SessionToken, map[string]any{})
	if !rematchAlice.OK || rematchAlice.RematchStatus != "waiting" || rematchAlice.AcceptedCount != 1 || rematchAlice.NewMatchID != "" {
		t.Fatalf("alice rematch invalid: %+v", rematchAlice)
	}
	rematchAliceDuplicate := postJSON[core.RematchResponse](t, server.URL+"/v1/matches/"+queueBob.MatchID+"/rematch", alice.SessionToken, map[string]any{})
	if rematchAliceDuplicate.AcceptedCount != 1 || rematchAliceDuplicate.RematchStatus != "waiting" {
		t.Fatalf("duplicate rematch should be idempotent: %+v", rematchAliceDuplicate)
	}
	rematchBob := postJSON[core.RematchResponse](t, server.URL+"/v1/matches/"+queueBob.MatchID+"/rematch", bob.SessionToken, map[string]any{})
	if rematchBob.RematchStatus != "found" || rematchBob.NewMatchID == "" || rematchBob.StageID != "lunar_maze" || !rematchBob.ServerAuthoritative {
		t.Fatalf("bob rematch invalid: %+v", rematchBob)
	}
	postJSON[core.ReadyResponse](t, server.URL+"/v1/matches/"+rematchBob.NewMatchID+"/ready", alice.SessionToken, map[string]any{})
	rematchReadyBob := postJSON[core.ReadyResponse](t, server.URL+"/v1/matches/"+rematchBob.NewMatchID+"/ready", bob.SessionToken, map[string]any{})
	if rematchReadyBob.MatchStart == nil || rematchReadyBob.MatchStart.StageID != "lunar_maze" || len(rematchReadyBob.MatchStart.Players) != 2 {
		t.Fatalf("rematch ready/start invalid: %+v", rematchReadyBob)
	}
	claim := postJSON[core.ActivityClaimResult](t, server.URL+"/v1/activity/claim", alice.SessionToken, map[string]any{
		"claim_kind": "task",
		"claim_id":   "daily_complete_match",
	})
	if !claim.OK || !claim.ServerAuthoritative || claim.SettlementKey == "" {
		t.Fatalf("claim invalid: %+v", claim)
	}
}

func TestHTTPRoomCodeFlow(t *testing.T) {
	service := core.NewService(core.Config{})
	server := httptest.NewServer(New(service))
	defer server.Close()

	host := postJSON[core.AuthSession](t, server.URL+"/v1/auth/anonymous", "", map[string]any{"device_id": "host", "display_name": "Host"})
	guest := postJSON[core.AuthSession](t, server.URL+"/v1/auth/anonymous", "", map[string]any{"device_id": "guest", "display_name": "Guest"})

	created := postJSON[core.QueueResponse](t, server.URL+"/v1/rooms/create", host.SessionToken, map[string]any{
		"mode_id":        "certification",
		"active_deck_id": "host_deck",
		"deck_snapshot":  validDeck("host_deck"),
	})
	if created.RoomCode == "" || created.RoomStatus != "waiting" || created.MatchID != "" {
		t.Fatalf("created room invalid: %+v", created)
	}
	list := getJSON[core.RoomListResponse](t, server.URL+"/v1/rooms", guest.SessionToken)
	if !list.OK || len(list.Rooms) != 1 || list.Rooms[0].RoomCode != created.RoomCode || !list.ServerAuthoritative {
		t.Fatalf("room list invalid: %+v", list)
	}
	rules := getJSON[core.RoomRulesSnapshot](t, server.URL+"/v1/rooms/"+created.RoomCode+"/rules", guest.SessionToken)
	if !rules.OK || rules.Room.RoomCode != created.RoomCode || rules.Mode.ModeID != "certification" || !rules.ServerAuthoritative {
		t.Fatalf("room rules invalid: %+v", rules)
	}
	if rules.HighFrequencyBattleTickAllowed || rules.ClientResultSubmit {
		t.Fatalf("HTTP room rules must not authorize business-channel battle tick or client result submit: %+v", rules)
	}
	if rules.Room.Participants[0].DeckSnapshotHash == "" || len(rules.ForbiddenFields) == 0 {
		t.Fatalf("room rules should expose hashes and forbidden fields: %+v", rules)
	}
	if !stringSliceContains(rules.ClientOperations, "matchmaking.cancel") || stringSliceContains(rules.ClientOperations, "battle.result.submit") {
		t.Fatalf("room rules should expose client ticket cancellation without result submit: %+v", rules)
	}
	if !stringSliceContains(rules.ClientOperations, "rooms.message") {
		t.Fatalf("room rules should expose lobby message contract: %+v", rules)
	}

	chat := postJSON[core.LobbyMessage](t, server.URL+"/v1/rooms/"+created.RoomCode+"/messages", host.SessionToken, map[string]any{
		"message_id": "http-room-chat-1",
		"kind":       "chat",
		"text":       "ready in lobby",
		"metadata":   map[string]any{"intent": "ready_check"},
	})
	if !chat.ServerAuthoritative || chat.RoomCode != created.RoomCode || chat.Kind != "chat" || chat.UserID != host.UserID || chat.Duplicate {
		t.Fatalf("chat lobby message invalid: %+v", chat)
	}
	pathScopedChat := postJSON[core.LobbyMessage](t, server.URL+"/v1/rooms/"+created.RoomCode+"/messages", host.SessionToken, map[string]any{
		"room_code":  "wrong-room",
		"message_id": "http-room-chat-path-scoped",
		"kind":       "chat",
		"text":       "path scoped",
	})
	if pathScopedChat.RoomCode != created.RoomCode || pathScopedChat.MessageID != "http-room-chat-path-scoped" {
		t.Fatalf("path room code should be authoritative for HTTP lobby messages: %+v", pathScopedChat)
	}
	duplicateChat := postJSON[core.LobbyMessage](t, server.URL+"/v1/rooms/"+created.RoomCode+"/messages", host.SessionToken, map[string]any{
		"message_id": "http-room-chat-1",
		"kind":       "chat",
		"text":       "duplicate should not replace",
	})
	if !duplicateChat.Duplicate || duplicateChat.Text != chat.Text {
		t.Fatalf("duplicate lobby message should return original: %+v", duplicateChat)
	}
	guestAnnouncement := postRaw(t, server.URL+"/v1/rooms/"+created.RoomCode+"/messages", guest.SessionToken, map[string]any{
		"message_id": "http-room-announcement-denied",
		"kind":       "announcement",
		"text":       "client announcement",
	})
	if guestAnnouncement.Code != http.StatusUnauthorized || guestAnnouncement.ErrorCode != "unauthorized" {
		t.Fatalf("non-host announcement should be rejected, got %+v", guestAnnouncement)
	}
	forbiddenMessage := postRaw(t, server.URL+"/v1/rooms/"+created.RoomCode+"/messages", host.SessionToken, map[string]any{
		"message_id": "http-room-forbidden-1",
		"text":       "bad metadata",
		"metadata":   map[string]any{"result_hash": "client-authored"},
	})
	if forbiddenMessage.Code != http.StatusForbidden || forbiddenMessage.ErrorCode != "forbidden_field" {
		t.Fatalf("lobby message must reject client-authored authority fields, got %+v", forbiddenMessage)
	}
	announcement := postJSON[core.LobbyMessage](t, server.URL+"/v1/rooms/"+created.RoomCode+"/messages", host.SessionToken, map[string]any{
		"message_id": "http-room-announcement-1",
		"kind":       "announcement",
		"text":       "match starting",
	})
	if announcement.Kind != "announcement" || announcement.UserID != host.UserID || !announcement.ServerAuthoritative {
		t.Fatalf("host announcement invalid: %+v", announcement)
	}
	roomSnapshot := getJSON[core.RoomSnapshot](t, server.URL+"/v1/rooms/"+created.RoomCode, guest.SessionToken)
	if len(roomSnapshot.Messages) != 3 || roomSnapshot.Messages[0].MessageID != chat.MessageID || roomSnapshot.Messages[1].MessageID != pathScopedChat.MessageID || roomSnapshot.Messages[2].MessageID != announcement.MessageID {
		t.Fatalf("room snapshot should include accepted lobby messages only: %+v", roomSnapshot.Messages)
	}

	joined := postJSON[core.QueueResponse](t, server.URL+"/v1/rooms/"+created.RoomCode+"/join", guest.SessionToken, map[string]any{
		"mode_id":        "certification",
		"active_deck_id": "guest_deck",
		"deck_snapshot":  validDeck("guest_deck"),
	})
	if joined.MatchID == "" || joined.RoomStatus != "found" {
		t.Fatalf("joined room invalid: %+v", joined)
	}

	hostTicket := getJSON[core.QueueResponse](t, server.URL+"/v1/matchmaking/tickets/"+created.TicketID, host.SessionToken)
	if hostTicket.MatchID != joined.MatchID || hostTicket.RoomCode != created.RoomCode {
		t.Fatalf("host ticket did not resolve room match: %+v", hostTicket)
	}

	leaveMatched := postRaw(t, server.URL+"/v1/rooms/"+created.RoomCode+"/leave", guest.SessionToken, map[string]any{})
	if leaveMatched.Code != http.StatusConflict || leaveMatched.ErrorCode != "match_state_invalid" {
		t.Fatalf("matched room leave should conflict, got %+v", leaveMatched)
	}
}

func TestHTTPBusinessEnvelopeFallbackGuard(t *testing.T) {
	service := core.NewService(core.Config{})
	server := httptest.NewServer(New(service))
	defer server.Close()

	alice := postJSON[core.AuthSession](t, server.URL+"/v1/auth/anonymous", "", map[string]any{"device_id": "envelope-a", "display_name": "Envelope A"})
	if alice.SessionToken == "" {
		t.Fatalf("expected auth session")
	}

	legacy := getJSON[core.BootstrapSnapshot](t, server.URL+"/v1/bootstrap", alice.SessionToken)
	if legacy.ServerVersion != core.ServerVersion {
		t.Fatalf("legacy bootstrap should remain compatible: %+v", legacy)
	}

	validHeaders := businessEnvelopeHeaders(1, time.Now(), "bootstrap-nonce-a", "bootstrap")
	secureBootstrap := getJSONWithHeaders[core.BootstrapSnapshot](t, server.URL+"/v1/bootstrap", alice.SessionToken, validHeaders)
	if secureBootstrap.ServerVersion != core.ServerVersion || !secureBootstrap.Inventory.OK {
		t.Fatalf("secure bootstrap invalid: %+v", secureBootstrap)
	}

	replayedSeq := getRawWithHeaders(t, server.URL+"/v1/inventory", alice.SessionToken, businessEnvelopeHeaders(1, time.Now(), "inventory-nonce-a", "inventory_read"))
	if replayedSeq.Code != http.StatusConflict || replayedSeq.ErrorCode != "business_envelope_replay" {
		t.Fatalf("expected replayed seq rejection, got %+v", replayedSeq)
	}

	replayedNonce := getRawWithHeaders(t, server.URL+"/v1/inventory", alice.SessionToken, businessEnvelopeHeaders(2, time.Now(), "bootstrap-nonce-a", "inventory_read"))
	if replayedNonce.Code != http.StatusConflict || replayedNonce.ErrorCode != "business_envelope_replay" {
		t.Fatalf("expected replayed nonce rejection, got %+v", replayedNonce)
	}

	stale := getRawWithHeaders(t, server.URL+"/v1/inventory", alice.SessionToken, businessEnvelopeHeaders(3, time.Now().Add(-10*time.Minute), "inventory-nonce-stale", "inventory_read"))
	if stale.Code != http.StatusConflict || stale.ErrorCode != "business_envelope_replay" {
		t.Fatalf("expected stale timestamp rejection, got %+v", stale)
	}

	missingToken := getRawWithHeaders(t, server.URL+"/v1/bootstrap", "", businessEnvelopeHeaders(1, time.Now(), "anonymous-nonce", "bootstrap"))
	if missingToken.Code != http.StatusUnauthorized || missingToken.ErrorCode != "business_envelope_invalid" {
		t.Fatalf("expected unauthenticated envelope rejection, got %+v", missingToken)
	}

	authWithEnvelope := postRawWithHeaders(t, server.URL+"/v1/auth/anonymous", "", map[string]any{"device_id": "envelope-public", "display_name": "Public Auth"}, businessEnvelopeHeaders(1, time.Now(), "auth-nonce", "auth_anonymous"))
	if authWithEnvelope.Code != http.StatusOK {
		t.Fatalf("auth endpoint should remain public during envelope migration, got %+v", authWithEnvelope)
	}

	status := getJSON[map[string]any](t, server.URL+"/v1/security/business-envelope", "")
	statusBody, ok := status["status"].(map[string]any)
	if !ok || statusBody["version"] != security.BusinessEnvelopeVersion || int(statusBody["accepted"].(float64)) != 1 || int(statusBody["rejected"].(float64)) < 4 {
		t.Fatalf("business envelope status invalid: %+v", status)
	}
}

func TestHTTPServiceCallbackStatusPublishesSharedContract(t *testing.T) {
	service := core.NewService(core.Config{})
	server := httptest.NewServer(New(service))
	defer server.Close()

	status := getJSON[map[string]any](t, server.URL+"/v1/security/service-callback", "")
	statusBody, ok := status["status"].(map[string]any)
	if !ok {
		t.Fatalf("service callback status missing body: %+v", status)
	}
	callbacks, ok := statusBody["service_callbacks"].([]any)
	if !ok || !anySliceContainsString(callbacks, "battle.result.submit") || !anySliceContainsString(callbacks, "battle.ticket.consume") {
		t.Fatalf("service callback status missing callback operations: %+v", statusBody)
	}
	disallowed, ok := statusBody["disallowed_client_operations"].([]any)
	if !ok || !anySliceContainsString(disallowed, "match.input") || !anySliceContainsString(disallowed, "battle.result.submit") || anySliceContainsString(disallowed, "battle.ticket") {
		t.Fatalf("service callback status missing disallowed client operation contract: %+v", statusBody)
	}
	topics, ok := statusBody["business_notification_topics"].([]any)
	if !ok || !anyBusinessNotificationTopicValid(topics, "battle.allocation") || !anyBusinessNotificationTopicValid(topics, "battle.ticket") || !anyBusinessNotificationTopicValid(topics, "settlement") || anyBusinessNotificationTopicValid(topics, "battle.result.submit") {
		t.Fatalf("service callback status missing low-frequency business notification topic contract: %+v", statusBody)
	}
	requestKinds, ok := statusBody["business_event_request_kinds"].([]any)
	if !ok || !reflect.DeepEqual(anyStrings(requestKinds), core.ContractBusinessEventRequestKinds()) || anySliceContainsString(requestKinds, "battle.result.submit") {
		t.Fatalf("service callback status missing business event request kind contract: %+v", statusBody)
	}
	context, ok := statusBody["service_callback_context"].(map[string]any)
	if !ok || context[serviceOriginContextKey] != core.ServiceCallbackContext()[serviceOriginContextKey] || context[serviceCallbackContextKey] != core.ServiceCallbackContext()[serviceCallbackContextKey] {
		t.Fatalf("service callback status drifted from core context: %+v", statusBody)
	}
	headers, ok := statusBody["http_headers"].(map[string]any)
	if !ok || headers["service_origin"] != headerServiceOrigin || headers["battle_callback"] != headerBattleCallback {
		t.Fatalf("service callback status missing HTTP header contract: %+v", statusBody)
	}
	values, ok := statusBody["accepted_callback_values"].([]any)
	if !ok || !anySliceContainsString(values, core.ServiceCallbackContext()[serviceCallbackContextKey]) || !anySliceContainsString(values, "1") || !anySliceContainsString(values, "yes") {
		t.Fatalf("service callback status missing accepted values: %+v", statusBody)
	}
	contractValues, ok := statusBody["service_callback_accepted_values"].([]any)
	if !ok || !reflect.DeepEqual(values, contractValues) {
		t.Fatalf("service callback status should expose business-contract accepted-value alias: %+v", statusBody)
	}
	for _, accepted := range core.ServiceCallbackAcceptedValues() {
		if !anySliceContainsString(values, accepted) {
			t.Fatalf("service callback status drifted from core accepted values: accepted=%+v status=%+v", core.ServiceCallbackAcceptedValues(), statusBody)
		}
	}
	if statusBody["player_session_context_allowed"] != false || statusBody["business_envelope_allowed"] != false {
		t.Fatalf("service callback status must keep callbacks out of player/envelope paths: %+v", statusBody)
	}
	if statusBody["service_callback_player_session_allowed"] != false || statusBody["service_callback_business_envelope_allowed"] != false {
		t.Fatalf("service callback status must expose business-contract callback booleans: %+v", statusBody)
	}
}

func TestHTTPServiceCallbacksRejectPlayerSessionContext(t *testing.T) {
	service := core.NewService(core.Config{})
	server := httptest.NewServer(New(service))
	defer server.Close()

	player := postJSON[core.AuthSession](t, server.URL+"/v1/auth/anonymous", "", map[string]any{"device_id": "http-service-player", "display_name": "HTTP Service Player"})
	routes := []string{
		"/v1/battle/servers/register",
		"/v1/battle/servers/heartbeat",
		"/v1/battle/servers/offline",
		"/v1/battle/tickets/consume",
		"/v1/battle/results/submit",
	}
	for _, route := range routes {
		response := postRaw(t, server.URL+route, player.SessionToken, map[string]any{})
		if response.Code != http.StatusForbidden || response.ErrorCode != "service_origin_required" || !strings.Contains(response.Message, "player session context") {
			t.Fatalf("service callback %s should reject player session context before core validation, got %+v", route, response)
		}
	}
}

func TestHTTPServiceCallbacksRejectBusinessEnvelopePayloadShape(t *testing.T) {
	service := core.NewService(core.Config{})
	server := httptest.NewServer(New(service))
	defer server.Close()

	routes := []string{
		"/v1/battle/servers/register",
		"/v1/battle/servers/heartbeat",
		"/v1/battle/servers/offline",
		"/v1/battle/tickets/consume",
		"/v1/battle/results/submit",
	}
	for index, route := range routes {
		response := postRaw(t, server.URL+route, "", map[string]any{
			security.BusinessEnvelopePayloadKey: map[string]any{
				"version":      security.BusinessEnvelopeVersion,
				"seq":          index + 1,
				"timestamp_ms": time.Now().UnixMilli(),
				"nonce":        "http-service-envelope-" + route,
				"op":           "service_callback",
				"key_id":       "dev-business-envelope-v0",
				"auth_tag":     strings.Repeat("0", 64),
				"mode":         "not_encrypted_http_fallback",
			},
		})
		if response.Code != http.StatusBadRequest || response.ErrorCode != "invalid_request" || !strings.Contains(response.Message, "business envelope") {
			t.Fatalf("service callback %s should reject envelope-shaped JSON before core validation, got %+v", route, response)
		}
	}

	for _, wrapper := range []string{"body", "request", "data"} {
		response := postRaw(t, server.URL+"/v1/battle/results/submit", "", map[string]any{
			wrapper: map[string]any{
				security.BusinessEnvelopePayloadKey: map[string]any{
					"version": security.BusinessEnvelopeVersion,
					"seq":     1,
				},
			},
		})
		if response.Code != http.StatusBadRequest || response.ErrorCode != "invalid_request" || !strings.Contains(response.Message, "business envelope") {
			t.Fatalf("service callback should reject nested %s envelope before core validation, got %+v", wrapper, response)
		}
	}

	for _, payload := range []map[string]any{
		{
			"version":       security.BusinessEnvelopeVersion,
			"signed_result": map[string]any{"match_id": "direct-version-client-shaped"},
		},
		{
			"seq":           1,
			"timestamp_ms":  time.Now().UnixMilli(),
			"nonce":         "http-service-direct-envelope",
			"op":            "battle_result_submit",
			"auth_tag":      strings.Repeat("1", 64),
			"signed_result": map[string]any{"match_id": "direct-client-shaped"},
		},
		{
			"body": map[string]any{
				"seq":           2,
				"timestamp_ms":  time.Now().UnixMilli(),
				"nonce":         "http-service-nested-direct-envelope",
				"op":            "battle_result_submit",
				"auth_tag":      strings.Repeat("2", 64),
				"signed_result": map[string]any{"match_id": "nested-direct-client-shaped"},
			},
		},
		{
			"key_id":        "dev-business-envelope-v0",
			"signed_result": map[string]any{"match_id": "key-id-client-shaped"},
		},
		{
			"request": map[string]any{
				"keyID":         "dev-business-envelope-v0",
				"signed_result": map[string]any{"match_id": "nested-key-id-client-shaped"},
			},
		},
		{
			"keyId":         "dev-business-envelope-v0",
			"signed_result": map[string]any{"match_id": "key-id-camel-client-shaped"},
		},
		{
			"business_session_id": "player-session",
			"signed_result":       map[string]any{"match_id": "business-session-client-shaped"},
		},
		{
			"data": map[string]any{
				"envelope_version": "business-v0-scaffold",
				"signed_result":    map[string]any{"match_id": "nested-envelope-version-client-shaped"},
			},
		},
		{
			"body": map[string]any{
				"version":       security.BusinessEnvelopeVersion,
				"signed_result": map[string]any{"match_id": "nested-version-client-shaped"},
			},
		},
	} {
		response := postRaw(t, server.URL+"/v1/battle/results/submit", "", payload)
		if response.Code != http.StatusBadRequest || response.ErrorCode != "invalid_request" || !strings.Contains(response.Message, "business envelope") {
			t.Fatalf("service callback should reject direct envelope fields before core validation, got %+v", response)
		}
	}
}

func TestHTTPServiceCallbacksRequireDevelopmentBattleCallbackHeaders(t *testing.T) {
	service := core.NewService(core.Config{})
	server := httptest.NewServer(New(service))
	defer server.Close()

	routes := []string{
		"/v1/battle/servers/register",
		"/v1/battle/servers/heartbeat",
		"/v1/battle/servers/offline",
		"/v1/battle/tickets/consume",
		"/v1/battle/results/submit",
	}
	for _, route := range routes {
		response := postRaw(t, server.URL+route, "", map[string]any{})
		if response.Code != http.StatusForbidden || response.ErrorCode != "service_origin_required" || !strings.Contains(response.Message, "development battle callback headers") {
			t.Fatalf("service callback %s should require development battle callback headers before core validation, got %+v", route, response)
		}
		for name, headers := range map[string]map[string]string{
			"wrong origin":     {headerServiceOrigin: "player", headerBattleCallback: "true"},
			"missing callback": {headerServiceOrigin: core.ServiceCallbackContext()[serviceOriginContextKey]},
			"false callback":   {headerServiceOrigin: core.ServiceCallbackContext()[serviceOriginContextKey], headerBattleCallback: "false"},
		} {
			response := postRawWithHeaders(t, server.URL+route, "", map[string]any{}, headers)
			if response.Code != http.StatusForbidden || response.ErrorCode != "service_origin_required" || !strings.Contains(response.Message, "development battle callback headers") {
				t.Fatalf("service callback %s should reject %s callback headers, got %+v", route, name, response)
			}
		}
		for _, callbackHeader := range []string{core.ServiceCallbackContext()[serviceCallbackContextKey], "1", "yes"} {
			response := postRawWithHeaders(t, server.URL+route, "", map[string]any{}, map[string]string{
				headerServiceOrigin:  core.ServiceCallbackContext()[serviceOriginContextKey],
				headerBattleCallback: callbackHeader,
			})
			if response.Code == http.StatusForbidden && response.ErrorCode == "service_origin_required" {
				t.Fatalf("service callback %s should accept development callback header %q before core validation, got %+v", route, callbackHeader, response)
			}
		}
		response = postRawWithHeaders(t, server.URL+route, "", map[string]any{}, map[string]string{
			headerServiceOrigin:  "  " + strings.ToUpper(core.ServiceCallbackContext()[serviceOriginContextKey]) + "  ",
			headerBattleCallback: "  " + strings.ToUpper(core.ServiceCallbackContext()[serviceCallbackContextKey]) + "  ",
		})
		if response.Code == http.StatusForbidden && response.ErrorCode == "service_origin_required" {
			t.Fatalf("service callback %s should normalize shared callback context headers, got %+v", route, response)
		}
	}
}

func TestHTTPServiceCallbacksBypassPlayerEnvelopeHeaderGuard(t *testing.T) {
	service := core.NewService(core.Config{})
	guard := security.NewBusinessEnvelopeGuard()
	server := httptest.NewServer(NewWithOptions(service, WithBusinessEnvelopeGuard(guard)))
	defer server.Close()

	player := postJSON[core.AuthSession](t, server.URL+"/v1/auth/anonymous", "", map[string]any{"device_id": "http-service-envelope-player", "display_name": "HTTP Service Envelope Player"})
	routes := []string{
		"/v1/battle/servers/register",
		"/v1/battle/servers/heartbeat",
		"/v1/battle/servers/offline",
		"/v1/battle/tickets/consume",
		"/v1/battle/results/submit",
	}
	for index, route := range routes {
		response := postRawWithHeaders(t, server.URL+route, player.SessionToken, map[string]any{}, businessEnvelopeHeaders(int64(index+1), time.Now(), "http-service-header-"+itoa(index), "service_callback"))
		if response.Code != http.StatusForbidden || response.ErrorCode != "service_origin_required" || !strings.Contains(response.Message, "player session context") {
			t.Fatalf("service callback %s should reject player context without player envelope guard, got %+v", route, response)
		}
	}
	if snapshot := guard.Snapshot(); snapshot.Accepted != 0 || snapshot.Rejected != 0 || len(snapshot.Audits) != 0 {
		t.Fatalf("service callback HTTP routes must not consume player business envelope guard state: %+v", snapshot)
	}
}

func TestHTTPServiceCallbacksRejectBusinessEnvelopeHeadersWithoutConsumingGuard(t *testing.T) {
	service := core.NewService(core.Config{})
	guard := security.NewBusinessEnvelopeGuard()
	server := httptest.NewServer(NewWithOptions(service, WithBusinessEnvelopeGuard(guard)))
	defer server.Close()

	routes := []string{
		"/v1/battle/servers/register",
		"/v1/battle/servers/heartbeat",
		"/v1/battle/servers/offline",
		"/v1/battle/tickets/consume",
		"/v1/battle/results/submit",
	}
	for index, route := range routes {
		response := postRawWithHeaders(t, server.URL+route, "", map[string]any{}, businessEnvelopeHeaders(int64(index+1), time.Now(), "http-service-no-token-header-"+itoa(index), "service_callback"))
		if response.Code != http.StatusBadRequest || response.ErrorCode != "invalid_request" || !strings.Contains(response.Message, "business envelope headers") {
			t.Fatalf("service callback %s should reject business envelope headers before core validation, got %+v", route, response)
		}
	}
	if snapshot := guard.Snapshot(); snapshot.Accepted != 0 || snapshot.Rejected != 0 || len(snapshot.Audits) != 0 {
		t.Fatalf("service callback envelope header rejection must not consume guard state: %+v", snapshot)
	}
}

func TestHTTPDatabaseWiringRecordsEnvelopeLobbyAndBattleAudits(t *testing.T) {
	driverName := registerHTTPAuditCaptureDriver(t)
	db, err := sql.Open(driverName, "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	wired, err := NewWithDatabase(db)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(wired.Handler)
	defer server.Close()

	battle := postJSONWithHeaders[core.BattleServerStatus](t, server.URL+"/v1/battle/servers/register", "", map[string]any{
		"battle_server_id": "http-sql-battle",
		"endpoint":         "127.0.0.1:7911",
		"region":           "local",
		"build_id":         "http-sql-test",
		"capacity":         4,
		"active_matches":   0,
		"load":             0,
		"status":           "online",
		"supported_modes":  []string{"pvp_duel"},
	}, serviceCallbackHeaders())
	if !battle.OK || battle.BattleServerID != "http-sql-battle" {
		t.Fatalf("battle server registration failed: %+v", battle)
	}
	alice := postJSON[core.AuthSession](t, server.URL+"/v1/auth/anonymous", "", map[string]any{"device_id": "http-sql-a", "display_name": "HTTP SQL A"})
	bob := postJSON[core.AuthSession](t, server.URL+"/v1/auth/anonymous", "", map[string]any{"device_id": "http-sql-b", "display_name": "HTTP SQL B"})

	room := postJSONWithHeaders[core.QueueResponse](t, server.URL+"/v1/rooms/create", alice.SessionToken, map[string]any{
		"mode_id":        "pvp_duel",
		"active_deck_id": "http_sql_deck_a",
		"deck_snapshot":  validDeck("http_sql_deck_a"),
		"mode_params":    map[string]any{"stage_id": "starlit_lanes", "character_id": "balanced"},
	}, businessEnvelopeHeaders(1, time.Now(), "http-sql-room-create", "rooms_create"))
	if room.RoomCode == "" {
		t.Fatalf("room create failed: %+v", room)
	}
	joined := postJSONWithHeaders[core.QueueResponse](t, server.URL+"/v1/rooms/"+room.RoomCode+"/join", bob.SessionToken, map[string]any{
		"mode_id":        "pvp_duel",
		"active_deck_id": "http_sql_deck_b",
		"deck_snapshot":  validDeck("http_sql_deck_b"),
	}, businessEnvelopeHeaders(1, time.Now(), "http-sql-room-join", "rooms_join"))
	if joined.MatchID == "" || joined.BattleTicket == nil || joined.BattleAllocation == nil {
		t.Fatalf("room join should allocate battle and ticket: %+v", joined)
	}

	battleStatus := wired.Service.BattleLifecycleAuditStatus()
	if !battleStatus.Configured || !battleStatus.OK || battleStatus.ServerLifecycleRecords != 1 || battleStatus.AllocationRecords != 1 || battleStatus.TicketRecords < 1 {
		t.Fatalf("battle audit status should reflect HTTP SQL repository writes: %+v", battleStatus)
	}
	lobbyStatus := wired.Service.LobbyLifecycleAuditStatus()
	if !lobbyStatus.Configured || !lobbyStatus.OK || lobbyStatus.RoomRecords != 3 {
		t.Fatalf("lobby audit status should reflect HTTP SQL repository writes: %+v", lobbyStatus)
	}
	tableCounts := httpAuditTableCounts()
	if tableCounts["business_envelope_audits"] != 2 || tableCounts["lobby_room_audits"] != 3 || tableCounts["match_allocation_audits"] != 2 || tableCounts["battle_ticket_audits"] < 1 {
		t.Fatalf("unexpected HTTP SQL audit writes: counts=%+v calls=%+v", tableCounts, httpAuditCaptureCalls())
	}

	battleAuditEndpoint := getJSON[map[string]any](t, server.URL+"/v1/security/battle-audit", "")
	if !battleAuditEndpoint["ok"].(bool) {
		t.Fatalf("battle audit endpoint should be public status: %+v", battleAuditEndpoint)
	}
	battleAuditStatus := battleAuditEndpoint["status"].(map[string]any)
	if !battleAuditStatus["configured"].(bool) || int(battleAuditStatus["allocation_records"].(float64)) != 1 || int(battleAuditStatus["server_lifecycle_records"].(float64)) != 1 {
		t.Fatalf("battle audit endpoint status invalid: %+v", battleAuditEndpoint)
	}
	lobbyAuditEndpoint := getJSON[map[string]any](t, server.URL+"/v1/security/lobby-audit", "")
	if !lobbyAuditEndpoint["ok"].(bool) {
		t.Fatalf("lobby audit endpoint should be public status: %+v", lobbyAuditEndpoint)
	}
	lobbyAuditStatus := lobbyAuditEndpoint["status"].(map[string]any)
	if !lobbyAuditStatus["configured"].(bool) || int(lobbyAuditStatus["room_records"].(float64)) != 3 {
		t.Fatalf("lobby audit endpoint status invalid: %+v", lobbyAuditEndpoint)
	}
}

func TestHTTPBattleServerAllocationAndTicketFlow(t *testing.T) {
	service := core.NewService(core.Config{})
	server := httptest.NewServer(New(service))
	defer server.Close()

	registered := postJSONWithHeaders[core.BattleServerStatus](t, server.URL+"/v1/battle/servers/register", "", map[string]any{
		"battle_server_id": "aaa-http-battle",
		"endpoint":         "127.0.0.1:7911",
		"region":           "local",
		"build_id":         "http-test",
		"capacity":         16,
		"status":           "online",
		"supported_modes":  []string{"certification"},
	}, serviceCallbackHeaders())
	if !registered.OK || registered.BattleServerID != "aaa-http-battle" || registered.Endpoint != "127.0.0.1:7911" {
		t.Fatalf("registered battle server invalid: %+v", registered)
	}
	registeredPVP := postJSONWithHeaders[core.BattleServerStatus](t, server.URL+"/v1/battle/servers/register", "", map[string]any{
		"battle_server_id": "aaa-http-pvp",
		"endpoint":         "127.0.0.1:7912",
		"region":           "local",
		"build_id":         "http-pvp-test",
		"capacity":         16,
		"status":           "online",
		"supported_modes":  []string{"pvp_duel"},
	}, serviceCallbackHeaders())
	if !registeredPVP.OK || registeredPVP.Status != "online" {
		t.Fatalf("registered pvp battle server invalid: %+v", registeredPVP)
	}
	offlinePVP := postJSONWithHeaders[core.BattleServerStatus](t, server.URL+"/v1/battle/servers/offline", "", map[string]any{
		"battle_server_id": "aaa-http-pvp",
		"status":           "online",
	}, serviceCallbackHeaders())
	if !offlinePVP.OK || offlinePVP.Status != "offline" || offlinePVP.Load != 0 || !offlinePVP.ServerAuthoritative {
		t.Fatalf("offline pvp battle server invalid: %+v", offlinePVP)
	}
	list := getJSON[core.BattleServerListResponse](t, server.URL+"/v1/battle/servers", "")
	if !list.OK || len(list.Servers) < 2 {
		t.Fatalf("battle server list invalid: %+v", list)
	}

	alice := postJSON[core.AuthSession](t, server.URL+"/v1/auth/anonymous", "", map[string]any{"device_id": "battle-http-a", "display_name": "Battle HTTP A"})
	bob := postJSON[core.AuthSession](t, server.URL+"/v1/auth/anonymous", "", map[string]any{"device_id": "battle-http-b", "display_name": "Battle HTTP B"})
	postJSON[core.QueueResponse](t, server.URL+"/v1/matchmaking/join", alice.SessionToken, map[string]any{
		"mode_id":        "certification",
		"active_deck_id": "http_battle_alice_deck",
		"deck_snapshot":  validDeck("http_battle_alice_deck"),
	})
	queueBob := postJSON[core.QueueResponse](t, server.URL+"/v1/matchmaking/join", bob.SessionToken, map[string]any{
		"mode_id":        "certification",
		"active_deck_id": "http_battle_bob_deck",
		"deck_snapshot":  validDeck("http_battle_bob_deck"),
	})
	if queueBob.MatchID == "" || queueBob.BattleAllocation == nil || queueBob.BattleTicket == nil {
		t.Fatalf("matched queue missing battle allocation/ticket: %+v", queueBob)
	}
	if queueBob.BattleAllocation.BattleServerID != "aaa-http-battle" || queueBob.BattleTicket.Ticket.Endpoint != "127.0.0.1:7911" {
		t.Fatalf("queue battle allocation/ticket mismatch: alloc=%+v ticket=%+v", queueBob.BattleAllocation, queueBob.BattleTicket)
	}

	pvpAlice := postJSON[core.AuthSession](t, server.URL+"/v1/auth/anonymous", "", map[string]any{"device_id": "battle-http-pvp-a", "display_name": "Battle HTTP PvP A"})
	pvpBob := postJSON[core.AuthSession](t, server.URL+"/v1/auth/anonymous", "", map[string]any{"device_id": "battle-http-pvp-b", "display_name": "Battle HTTP PvP B"})
	postJSON[core.QueueResponse](t, server.URL+"/v1/matchmaking/join", pvpAlice.SessionToken, map[string]any{
		"mode_id":        "pvp_duel",
		"active_deck_id": "http_pvp_alice_deck",
		"deck_snapshot":  validDeck("http_pvp_alice_deck"),
	})
	pvpMatch := postJSON[core.QueueResponse](t, server.URL+"/v1/matchmaking/join", pvpBob.SessionToken, map[string]any{
		"mode_id":        "pvp_duel",
		"active_deck_id": "http_pvp_bob_deck",
		"deck_snapshot":  validDeck("http_pvp_bob_deck"),
	})
	if pvpMatch.MatchID == "" || pvpMatch.BattleAllocation == nil || pvpMatch.BattleAllocation.BattleServerID == "aaa-http-pvp" {
		t.Fatalf("offline pvp battle server must be skipped for future allocation: %+v", pvpMatch)
	}

	allocation := getJSON[core.BattleServerAllocation](t, server.URL+"/v1/matches/"+queueBob.MatchID+"/battle-allocation", alice.SessionToken)
	if !allocation.OK || allocation.MatchID != queueBob.MatchID || len(allocation.Players) != 2 || allocation.Version.ProtocolVersion != core.ProtocolVersion {
		t.Fatalf("explicit allocation invalid: %+v", allocation)
	}
	ticket := postJSON[core.SignedBattleTicket](t, server.URL+"/v1/matches/"+queueBob.MatchID+"/battle-ticket", alice.SessionToken, map[string]any{})
	if !ticket.OK || ticket.Ticket.UserID != alice.UserID || ticket.Ticket.BattleServerID != "aaa-http-battle" || ticket.SignatureHex == "" || ticket.PublicKeyHex == "" {
		t.Fatalf("explicit battle ticket invalid: %+v", ticket)
	}
	consume := postJSONWithHeaders[core.BattleTicketConsumeResponse](t, server.URL+"/v1/battle/tickets/consume", "", map[string]any{
		"version":          ticket.Ticket.Version,
		"ticket_id":        ticket.Ticket.TicketID,
		"match_id":         queueBob.MatchID,
		"user_id":          alice.UserID,
		"player_id":        ticket.Ticket.PlayerID,
		"battle_server_id": ticket.Ticket.BattleServerID,
		"mode_config_hash": ticket.Ticket.ModeConfigHash,
		"ticket_nonce_hex": ticket.Ticket.TicketNonceHex,
	}, serviceCallbackHeaders())
	if !consume.OK || !consume.Consumed || consume.Duplicate || consume.TicketID != ticket.Ticket.TicketID || !consume.ServerAuthoritative {
		t.Fatalf("battle ticket consume invalid: %+v", consume)
	}
	if consume.IssuedAtMS != ticket.Ticket.IssuedAtMS || consume.ExpiresAtMS != ticket.Ticket.ExpiresAtMS || consume.ConsumedAtMS == 0 {
		t.Fatalf("battle ticket consume should echo ticket lifecycle timestamps: ticket=%+v consume=%+v", ticket.Ticket, consume)
	}
	duplicateConsume := postJSONWithHeaders[core.BattleTicketConsumeResponse](t, server.URL+"/v1/battle/tickets/consume", "", map[string]any{
		"version":          ticket.Ticket.Version,
		"ticket_id":        ticket.Ticket.TicketID,
		"match_id":         queueBob.MatchID,
		"battle_server_id": ticket.Ticket.BattleServerID,
		"mode_config_hash": ticket.Ticket.ModeConfigHash,
		"ticket_nonce_hex": ticket.Ticket.TicketNonceHex,
	}, serviceCallbackHeaders())
	if !duplicateConsume.OK || !duplicateConsume.Consumed || !duplicateConsume.Duplicate || !duplicateConsume.ServerAuthoritative {
		t.Fatalf("duplicate battle ticket consume invalid: %+v", duplicateConsume)
	}
	if duplicateConsume.ConsumedAtMS != consume.ConsumedAtMS || duplicateConsume.ExpiresAtMS != consume.ExpiresAtMS {
		t.Fatalf("duplicate consume should preserve first consume lifecycle timestamps: first=%+v duplicate=%+v", consume, duplicateConsume)
	}
	badConsume := postRawWithHeaders(t, server.URL+"/v1/battle/tickets/consume", "", map[string]any{
		"version":          ticket.Ticket.Version,
		"ticket_id":        ticket.Ticket.TicketID,
		"match_id":         queueBob.MatchID,
		"battle_server_id": ticket.Ticket.BattleServerID,
		"ticket_nonce_hex": "wrong-nonce",
	}, serviceCallbackHeaders())
	if badConsume.Code != http.StatusBadRequest || badConsume.ErrorCode != "invalid_request" {
		t.Fatalf("expected bad ticket consume rejection, got %+v", badConsume)
	}
	staleVersion := ticket.Ticket.Version
	staleVersion.RulesetVersion = "ruleset-stale"
	staleConsume := postRawWithHeaders(t, server.URL+"/v1/battle/tickets/consume", "", map[string]any{
		"version":          staleVersion,
		"ticket_id":        ticket.Ticket.TicketID,
		"match_id":         queueBob.MatchID,
		"battle_server_id": ticket.Ticket.BattleServerID,
		"ticket_nonce_hex": ticket.Ticket.TicketNonceHex,
	}, serviceCallbackHeaders())
	if staleConsume.Code != http.StatusBadRequest || staleConsume.ErrorCode != "invalid_request" {
		t.Fatalf("expected stale ticket consume version rejection, got %+v", staleConsume)
	}

	postJSON[core.ReadyResponse](t, server.URL+"/v1/matches/"+queueBob.MatchID+"/ready", alice.SessionToken, map[string]any{})
	readyBob := postJSON[core.ReadyResponse](t, server.URL+"/v1/matches/"+queueBob.MatchID+"/ready", bob.SessionToken, map[string]any{})
	if readyBob.MatchStart == nil || readyBob.MatchStart.BattleAllocation == nil || readyBob.BattleTicket == nil || readyBob.BattleTicket.Ticket.MatchID != queueBob.MatchID {
		t.Fatalf("ready should include battle allocation/ticket: %+v", readyBob)
	}

	playerIDs := make([]string, 0, len(queueBob.BattleAllocation.Players))
	for _, player := range queueBob.BattleAllocation.Players {
		playerIDs = append(playerIDs, player.PlayerID)
	}
	result := core.SignedBattleResult{
		OK: true,
		Result: core.BattleResult{
			Version:     queueBob.BattleAllocation.Version,
			MatchID:     queueBob.MatchID,
			ModeID:      "certification",
			ResultHash:  "sha256:abcdef1234567890",
			ReplayID:    "battle_http_replay_" + queueBob.MatchID,
			PlayerIDs:   playerIDs,
			SettledAtMS: time.Now().UnixMilli(),
		},
		SignatureAlg:        "ED25519",
		KeyID:               "aaa-http-battle",
		SignatureHex:        "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		PublicKeyHex:        "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		ServerAuthoritative: true,
	}
	badProjection := result
	badProjection.Result.RewardProjectionJSON = `{"source":"battle_server","reward":{"gold":999999}}`
	forbiddenProjection := postRawWithHeaders(t, server.URL+"/v1/battle/results/submit", "", core.BattleResultSubmitRequest{SignedResult: badProjection}, serviceCallbackHeaders())
	if forbiddenProjection.Code != http.StatusForbidden || forbiddenProjection.ErrorCode != "forbidden_field" {
		t.Fatalf("expected forbidden battle result projection rejection, got %+v", forbiddenProjection)
	}

	submit := postJSONWithHeaders[core.BattleResultSubmitResponse](t, server.URL+"/v1/battle/results/submit", "", core.BattleResultSubmitRequest{SignedResult: result}, serviceCallbackHeaders())
	if !submit.OK || !submit.Accepted || submit.MatchID != queueBob.MatchID {
		t.Fatalf("battle result submit invalid: %+v", submit)
	}
}

func TestHTTPMatchEntryRejectsIncompatibleClientVersion(t *testing.T) {
	service := core.NewService(core.Config{})
	server := httptest.NewServer(New(service))
	defer server.Close()

	alice := postJSON[core.AuthSession](t, server.URL+"/v1/auth/anonymous", "", map[string]any{"device_id": "http-version-a", "display_name": "HTTP Version A"})
	rejected := postRaw(t, server.URL+"/v1/matchmaking/join", alice.SessionToken, map[string]any{
		"mode_id":        "pvp_duel",
		"active_deck_id": "http_version_a_deck",
		"deck_snapshot":  validDeck("http_version_a_deck"),
		"client_version": map[string]any{"protocol_version": core.ProtocolVersion + 1},
	})
	if rejected.Code != http.StatusBadRequest || rejected.ErrorCode != "mode_invalid" {
		t.Fatalf("expected protocol mismatch rejection, got %+v", rejected)
	}
	partialVersion := postRaw(t, server.URL+"/v1/matchmaking/join", alice.SessionToken, map[string]any{
		"mode_id":        "pvp_duel",
		"active_deck_id": "http_version_a_deck",
		"deck_snapshot":  validDeck("http_version_a_deck"),
		"client_version": map[string]any{"protocol_version": core.ProtocolVersion},
	})
	if partialVersion.Code != http.StatusBadRequest || partialVersion.ErrorCode != "mode_invalid" {
		t.Fatalf("expected partial version rejection, got %+v", partialVersion)
	}

	host := postJSON[core.AuthSession](t, server.URL+"/v1/auth/anonymous", "", map[string]any{"device_id": "http-version-host", "display_name": "HTTP Version Host"})
	room := postJSON[core.QueueResponse](t, server.URL+"/v1/rooms/create", host.SessionToken, map[string]any{
		"mode_id":        "pvp_duel",
		"active_deck_id": "http_version_host_deck",
		"deck_snapshot":  validDeck("http_version_host_deck"),
		"client_version": map[string]any{
			"protocol_version":     core.ProtocolVersion,
			"business_api_version": core.BusinessAPIVersion,
			"battle_api_version":   core.BattleAPIVersion,
			"ruleset_version":      core.RulesetVersion,
		},
	})
	if room.RoomCode == "" {
		t.Fatalf("expected compatible room create, got %+v", room)
	}

	guest := postJSON[core.AuthSession](t, server.URL+"/v1/auth/anonymous", "", map[string]any{"device_id": "http-version-guest", "display_name": "HTTP Version Guest"})
	roomJoin := postRaw(t, server.URL+"/v1/rooms/"+room.RoomCode+"/join", guest.SessionToken, map[string]any{
		"mode_id":        "pvp_duel",
		"active_deck_id": "http_version_guest_deck",
		"deck_snapshot":  validDeck("http_version_guest_deck"),
		"client_version": map[string]any{"ruleset_version": "ruleset-old"},
	})
	if roomJoin.Code != http.StatusBadRequest || roomJoin.ErrorCode != "mode_invalid" {
		t.Fatalf("expected ruleset mismatch rejection, got %+v", roomJoin)
	}
}

func TestHTTPCancelTicketFlow(t *testing.T) {
	service := core.NewService(core.Config{})
	server := httptest.NewServer(New(service))
	defer server.Close()

	alice := postJSON[core.AuthSession](t, server.URL+"/v1/auth/anonymous", "", map[string]any{"device_id": "cancel-a", "display_name": "Cancel A"})
	bob := postJSON[core.AuthSession](t, server.URL+"/v1/auth/anonymous", "", map[string]any{"device_id": "cancel-b", "display_name": "Cancel B"})
	cara := postJSON[core.AuthSession](t, server.URL+"/v1/auth/anonymous", "", map[string]any{"device_id": "cancel-c", "display_name": "Cancel C"})

	aliceQueue := postJSON[core.QueueResponse](t, server.URL+"/v1/matchmaking/join", alice.SessionToken, map[string]any{
		"mode_id":        "certification",
		"active_deck_id": "cancel_alice_deck",
		"deck_snapshot":  validDeck("cancel_alice_deck"),
	})
	cancelled := postJSON[core.QueueResponse](t, server.URL+"/v1/matchmaking/tickets/"+aliceQueue.TicketID+"/cancel", alice.SessionToken, map[string]any{})
	if cancelled.QueueStatus != "cancelled" || cancelled.CurrentPlayers != 0 || cancelled.MatchID != "" {
		t.Fatalf("cancel response invalid: %+v", cancelled)
	}
	bobQueue := postJSON[core.QueueResponse](t, server.URL+"/v1/matchmaking/join", bob.SessionToken, map[string]any{
		"mode_id":        "certification",
		"active_deck_id": "cancel_bob_deck",
		"deck_snapshot":  validDeck("cancel_bob_deck"),
	})
	caraQueue := postJSON[core.QueueResponse](t, server.URL+"/v1/matchmaking/join", cara.SessionToken, map[string]any{
		"mode_id":        "certification",
		"active_deck_id": "cancel_cara_deck",
		"deck_snapshot":  validDeck("cancel_cara_deck"),
	})
	if bobQueue.MatchID != "" || caraQueue.MatchID == "" {
		t.Fatalf("cancelled ticket should not consume match slot: bob=%+v cara=%+v", bobQueue, caraQueue)
	}
	matchedCancel := postRaw(t, server.URL+"/v1/matchmaking/tickets/"+caraQueue.TicketID+"/cancel", cara.SessionToken, map[string]any{})
	if matchedCancel.Code != http.StatusConflict || matchedCancel.ErrorCode != "match_state_invalid" {
		t.Fatalf("expected matched cancel conflict, got %+v", matchedCancel)
	}
}

func TestHTTPHeartbeatFlow(t *testing.T) {
	service := core.NewService(core.Config{})
	server := httptest.NewServer(New(service))
	defer server.Close()

	alice := postJSON[core.AuthSession](t, server.URL+"/v1/auth/anonymous", "", map[string]any{"device_id": "heartbeat-a", "display_name": "Heartbeat A"})
	bob := postJSON[core.AuthSession](t, server.URL+"/v1/auth/anonymous", "", map[string]any{"device_id": "heartbeat-b", "display_name": "Heartbeat B"})

	aliceQueue := postJSON[core.QueueResponse](t, server.URL+"/v1/matchmaking/join", alice.SessionToken, map[string]any{
		"mode_id":        "certification",
		"active_deck_id": "heartbeat_alice_deck",
		"deck_snapshot":  validDeck("heartbeat_alice_deck"),
	})
	waitBeat := postJSON[core.PresenceHeartbeatResponse](t, server.URL+"/v1/presence/heartbeat", alice.SessionToken, map[string]any{
		"ticket_id":         aliceQueue.TicketID,
		"client_tick":       7,
		"last_event_cursor": -10,
	})
	if waitBeat.PresenceStatus != "queue_waiting" || waitBeat.QueueStatus != "queued" || waitBeat.CurrentPlayers != 1 || waitBeat.LastEventCursor != 0 {
		t.Fatalf("waiting heartbeat invalid: %+v", waitBeat)
	}
	bobQueue := postJSON[core.QueueResponse](t, server.URL+"/v1/matchmaking/join", bob.SessionToken, map[string]any{
		"mode_id":        "certification",
		"active_deck_id": "heartbeat_bob_deck",
		"deck_snapshot":  validDeck("heartbeat_bob_deck"),
	})
	if bobQueue.MatchID == "" {
		t.Fatalf("expected match: %+v", bobQueue)
	}
	postJSON[core.ReadyResponse](t, server.URL+"/v1/matches/"+bobQueue.MatchID+"/ready", alice.SessionToken, map[string]any{})
	postJSON[core.ReadyResponse](t, server.URL+"/v1/matches/"+bobQueue.MatchID+"/ready", bob.SessionToken, map[string]any{})
	postJSON[core.InputResponse](t, server.URL+"/v1/matches/"+bobQueue.MatchID+"/input", alice.SessionToken, map[string]any{
		"tick": 1, "seq": 1, "dir": 0, "slow": false, "shoot": true, "bomb": false, "card_slot": -1,
	})
	matchBeat := postJSON[core.PresenceHeartbeatResponse](t, server.URL+"/v1/presence/heartbeat", alice.SessionToken, map[string]any{
		"ticket_id":         aliceQueue.TicketID,
		"match_id":          bobQueue.MatchID,
		"client_tick":       2,
		"last_event_cursor": 1,
	})
	if matchBeat.PresenceStatus != "in_match" || matchBeat.MatchStatus != "running" || !matchBeat.Connected || matchBeat.MatchTick < 1 || matchBeat.LatestEventCursor <= 1 || !matchBeat.ServerAuthoritative {
		t.Fatalf("match heartbeat invalid: %+v", matchBeat)
	}
	if matchBeat.BattleAllocation == nil || matchBeat.BattleAllocation.MatchID != bobQueue.MatchID || matchBeat.BattleTicket == nil || matchBeat.BattleTicket.Ticket.UserID != alice.UserID || matchBeat.BattleTicket.SignatureHex == "" {
		t.Fatalf("match heartbeat should include low-frequency allocation and signed ticket descriptors: %+v", matchBeat)
	}
	postJSON[core.ReconnectResponse](t, server.URL+"/v1/matches/"+bobQueue.MatchID+"/disconnect", alice.SessionToken, map[string]any{})
	disconnectBeat := postJSON[core.PresenceHeartbeatResponse](t, server.URL+"/v1/presence/heartbeat", alice.SessionToken, map[string]any{
		"match_id": bobQueue.MatchID,
	})
	if disconnectBeat.PresenceStatus != "disconnected" || disconnectBeat.Connected || disconnectBeat.ReconnectSecondsLeft <= 0 {
		t.Fatalf("disconnect heartbeat invalid: %+v", disconnectBeat)
	}
	if disconnectBeat.BattleAllocation == nil || disconnectBeat.BattleTicket == nil || disconnectBeat.BattleTicket.Ticket.MatchID != bobQueue.MatchID {
		t.Fatalf("disconnect heartbeat should preserve reconnect allocation/ticket descriptors: %+v", disconnectBeat)
	}
}

func TestHTTPModeActionFlow(t *testing.T) {
	service := core.NewService(core.Config{})
	server := httptest.NewServer(New(service))
	defer server.Close()

	sessions := []core.AuthSession{}
	for i := 0; i < 5; i++ {
		session := postJSON[core.AuthSession](t, server.URL+"/v1/auth/anonymous", "", map[string]any{"device_id": fmt.Sprintf("br-%d", i), "display_name": fmt.Sprintf("BR %d", i)})
		sessions = append(sessions, session)
	}
	matchID := ""
	for i, session := range sessions {
		queue := postJSON[core.QueueResponse](t, server.URL+"/v1/matchmaking/join", session.SessionToken, map[string]any{
			"mode_id":        "battle_royale",
			"active_deck_id": fmt.Sprintf("br_deck_%d", i),
			"deck_snapshot":  validDeck(fmt.Sprintf("br_deck_%d", i)),
		})
		if queue.MatchID != "" {
			matchID = queue.MatchID
		}
	}
	if matchID == "" {
		t.Fatalf("expected battle royale match")
	}
	candidate := ""
	for _, session := range sessions {
		ready := postJSON[core.ReadyResponse](t, server.URL+"/v1/matches/"+matchID+"/ready", session.SessionToken, map[string]any{})
		if ready.MatchStart != nil {
			if candidates, ok := ready.MatchStart.ModeState["candidate_cards"].([]any); ok && len(candidates) > 0 {
				candidate = fmt.Sprint(candidates[0])
			}
			if candidates, ok := ready.MatchStart.ModeState["candidate_cards"].([]string); ok && len(candidates) > 0 {
				candidate = candidates[0]
			}
		}
	}
	if candidate == "" {
		candidate = "focus_lens"
	}
	resp := postJSON[core.ModeActionResponse](t, server.URL+"/v1/matches/"+matchID+"/mode-action", sessions[0].SessionToken, map[string]any{
		"mode_id":     "battle_royale",
		"action_type": "select_round_card",
		"payload": map[string]any{
			"card_id":     candidate,
			"round_index": 0,
		},
	})
	if !resp.OK || !resp.Accepted || resp.ActionType != "select_round_card" || resp.Event.Type != "mode_action_accepted" || !resp.ServerAuthoritative {
		t.Fatalf("mode action response invalid: %+v", resp)
	}
	forbidden := postRaw(t, server.URL+"/v1/matches/"+matchID+"/mode-action", sessions[1].SessionToken, map[string]any{
		"mode_id":                     "battle_royale",
		"action_type":                 "select_round_card",
		"client_result_authoritative": true,
		"payload":                     map[string]any{"card_id": "density_surge", "round_index": 0},
	})
	if forbidden.Code != http.StatusForbidden || forbidden.ErrorCode != "forbidden_field" {
		t.Fatalf("expected forbidden client authority, got %+v", forbidden)
	}
}

type errorBody struct {
	OK        bool   `json:"ok"`
	ErrorCode string `json:"error_code"`
	Message   string `json:"message"`
	Code      int
}

func postJSON[T any](t *testing.T, url string, token string, body any) T {
	t.Helper()
	raw, _ := json.Marshal(body)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var errorResp errorBody
		_ = json.NewDecoder(resp.Body).Decode(&errorResp)
		t.Fatalf("POST %s returned %d %+v", url, resp.StatusCode, errorResp)
	}
	var out T
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

func postJSONWithHeaders[T any](t *testing.T, url string, token string, body any, headers map[string]string) T {
	t.Helper()
	raw, _ := json.Marshal(body)
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
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var errBody errorBody
		_ = json.NewDecoder(resp.Body).Decode(&errBody)
		t.Fatalf("unexpected status %d for %s: %+v", resp.StatusCode, url, errBody)
	}
	var out T
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

func getJSON[T any](t *testing.T, url string, token string) T {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s returned %d", url, resp.StatusCode)
	}
	var out T
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

func getJSONWithHeaders[T any](t *testing.T, url string, token string, headers map[string]string) T {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
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
	if resp.StatusCode != http.StatusOK {
		var errorResp errorBody
		_ = json.NewDecoder(resp.Body).Decode(&errorResp)
		t.Fatalf("GET %s returned %d %+v", url, resp.StatusCode, errorResp)
	}
	var out T
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

func getRaw(t *testing.T, url string, token string) errorBody {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
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

func getRawWithHeaders(t *testing.T, url string, token string, headers map[string]string) errorBody {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
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

func postRaw(t *testing.T, url string, token string, body any) errorBody {
	t.Helper()
	raw, _ := json.Marshal(body)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
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

func postRawWithHeaders(t *testing.T, url string, token string, body any, headers map[string]string) errorBody {
	t.Helper()
	raw, _ := json.Marshal(body)
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

func businessEnvelopeHeaders(seq int64, timestamp time.Time, nonce string, op string) map[string]string {
	return map[string]string{
		security.HeaderBusinessEnvelope:    security.BusinessEnvelopeVersion,
		security.HeaderBusinessSeq:         fmt.Sprintf("%d", seq),
		security.HeaderBusinessTimestampMs: fmt.Sprintf("%d", timestamp.UnixMilli()),
		security.HeaderBusinessNonce:       nonce,
		security.HeaderBusinessOp:          op,
		security.HeaderBusinessKeyID:       "dev-business-envelope-v0",
		security.HeaderBusinessTag:         "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		security.HeaderBusinessMode:        "not_encrypted_http_fallback",
	}
}

func serviceCallbackHeaders() map[string]string {
	return map[string]string{
		headerServiceOrigin:  core.ServiceCallbackContext()[serviceOriginContextKey],
		headerBattleCallback: core.ServiceCallbackContext()[serviceCallbackContextKey],
	}
}

func hasEventType(events []core.MatchEvent, eventType string) bool {
	for _, event := range events {
		if event.Type == eventType {
			return true
		}
	}
	return false
}

func anySliceContainsString(values []any, target string) bool {
	for _, value := range values {
		if text, ok := value.(string); ok && text == target {
			return true
		}
	}
	return false
}

func anyStrings(values []any) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if text, ok := value.(string); ok {
			out = append(out, text)
		}
	}
	return out
}

func itoa(value int) string {
	return fmt.Sprintf("%d", value)
}

func validDeck(deckID string) core.DeckSnapshot {
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

type httpAuditCaptureCall struct {
	query string
	args  []any
}

var httpAuditCaptureState = struct {
	sync.Mutex
	nextID int
	calls  []httpAuditCaptureCall
}{}

func registerHTTPAuditCaptureDriver(t *testing.T) string {
	t.Helper()
	httpAuditCaptureState.Lock()
	defer httpAuditCaptureState.Unlock()
	httpAuditCaptureState.nextID++
	httpAuditCaptureState.calls = nil
	name := fmt.Sprintf("http_audit_capture_driver_%d", httpAuditCaptureState.nextID)
	sql.Register(name, httpAuditCaptureDriver{})
	return name
}

func httpAuditCaptureCalls() []httpAuditCaptureCall {
	httpAuditCaptureState.Lock()
	defer httpAuditCaptureState.Unlock()
	calls := make([]httpAuditCaptureCall, len(httpAuditCaptureState.calls))
	copy(calls, httpAuditCaptureState.calls)
	return calls
}

func httpAuditTableCounts() map[string]int {
	counts := map[string]int{}
	for _, call := range httpAuditCaptureCalls() {
		for _, table := range []string{
			"business_envelope_audits",
			"lobby_room_audits",
			"match_allocation_audits",
			"battle_ticket_audits",
		} {
			if strings.Contains(call.query, "INSERT INTO "+table) {
				counts[table]++
			}
		}
	}
	return counts
}

func stringSliceContains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func anyBusinessNotificationTopicValid(topics []any, want string) bool {
	for _, topic := range topics {
		fields, ok := topic.(map[string]any)
		if !ok || fields["kind"] != want {
			continue
		}
		topicName, _ := fields["topic"].(string)
		transport, _ := fields["transport"].(string)
		requestOp, _ := fields["client_event_request_operation"].(string)
		expectedRequestOp := "business.event"
		if want == "settlement" {
			expectedRequestOp = "business.event.settlement"
		}
		if topicName != "nakama_wss.business."+strings.ReplaceAll(want, ".", "_") || transport != "nakama_wss" || requestOp != expectedRequestOp {
			return false
		}
		return fields["server_push"] == true &&
			fields["service_callback"] == false &&
			fields["high_frequency_battle_tick_allowed"] == false &&
			fields["client_result_submit_allowed"] == false
	}
	return false
}

type httpAuditCaptureDriver struct{}

func (httpAuditCaptureDriver) Open(name string) (driver.Conn, error) {
	return httpAuditCaptureConn{}, nil
}

type httpAuditCaptureConn struct{}

func (httpAuditCaptureConn) Prepare(query string) (driver.Stmt, error) {
	return httpAuditCaptureStmt{query: query}, nil
}

func (httpAuditCaptureConn) Close() error {
	return nil
}

func (httpAuditCaptureConn) Begin() (driver.Tx, error) {
	return httpAuditCaptureTx{}, nil
}

type httpAuditCaptureTx struct{}

func (httpAuditCaptureTx) Commit() error {
	return nil
}

func (httpAuditCaptureTx) Rollback() error {
	return nil
}

type httpAuditCaptureStmt struct {
	query string
}

func (stmt httpAuditCaptureStmt) Close() error {
	return nil
}

func (stmt httpAuditCaptureStmt) NumInput() int {
	return -1
}

func (stmt httpAuditCaptureStmt) Exec(args []driver.Value) (driver.Result, error) {
	values := make([]any, len(args))
	for index, arg := range args {
		values[index] = arg
	}
	httpAuditCaptureState.Lock()
	httpAuditCaptureState.calls = append(httpAuditCaptureState.calls, httpAuditCaptureCall{
		query: stmt.query,
		args:  values,
	})
	httpAuditCaptureState.Unlock()
	return driver.RowsAffected(1), nil
}

func (stmt httpAuditCaptureStmt) Query(args []driver.Value) (driver.Rows, error) {
	return httpAuditCaptureRows{}, nil
}

type httpAuditCaptureRows struct{}

func (httpAuditCaptureRows) Columns() []string {
	return []string{}
}

func (httpAuditCaptureRows) Close() error {
	return nil
}

func (httpAuditCaptureRows) Next(dest []driver.Value) error {
	return io.EOF
}
