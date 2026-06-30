package nakamaapi

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"gensoulkyo/runtime/core"
	"gensoulkyo/runtime/security"
)

func TestNakamaRPCRequiresEnvelopeForAuthenticatedBusinessCalls(t *testing.T) {
	handler := New(core.NewService(core.Config{}))
	login := handler.HandleRPC(RPCRequest{
		ID:      "auth.anonymous",
		Payload: map[string]any{"device_id": "device-rpc", "display_name": "RPC Player"},
	})
	if !login.OK || login.Status != 200 {
		t.Fatalf("login failed: %+v", login)
	}
	session := login.Payload.(*core.AuthSession)

	missing := handler.HandleRPC(RPCRequest{ID: "bootstrap", SessionID: session.SessionToken, UserID: session.UserID, Payload: map[string]any{}})
	if missing.OK || missing.Status != 400 || missing.ErrorCode != CodeBusinessEnvelopeRequired {
		t.Fatalf("expected required envelope rejection, got %+v", missing)
	}

	accepted := handler.HandleRPC(RPCRequest{
		ID:        "bootstrap",
		SessionID: session.SessionToken,
		UserID:    session.UserID,
		Payload:   envelopePayload(1, "nonce-rpc-bootstrap", "bootstrap", map[string]any{}),
	})
	if !accepted.OK || accepted.Status != 200 {
		t.Fatalf("expected bootstrap through envelope, got %+v", accepted)
	}
	if _, ok := accepted.Payload.(*core.BootstrapSnapshot); !ok {
		t.Fatalf("expected bootstrap payload, got %T", accepted.Payload)
	}

	replay := handler.HandleRPC(RPCRequest{
		ID:        "inventory.get",
		SessionID: session.SessionToken,
		UserID:    session.UserID,
		Payload:   envelopePayload(1, "nonce-rpc-inventory", "inventory_get", map[string]any{}),
	})
	if replay.OK || replay.Status != 409 || replay.ErrorCode != security.CodeBusinessEnvelopeReplay {
		t.Fatalf("expected shared guard seq replay rejection, got %+v", replay)
	}

	snapshot := handler.EnvelopeSnapshot()
	if snapshot.Accepted != 1 || snapshot.Rejected != 2 || len(snapshot.Audits) != 3 {
		t.Fatalf("unexpected envelope snapshot: %+v", snapshot)
	}
	if snapshot.Audits[1].Transport != security.BusinessEnvelopeTransportNakamaRPC || snapshot.Audits[1].Endpoint != "rpc.bootstrap" {
		t.Fatalf("expected rpc audit metadata, got %+v", snapshot.Audits[1])
	}
}

func TestNakamaRPCRejectsMalformedBindingPayloadBeforeDispatch(t *testing.T) {
	handler := New(core.NewService(core.Config{}))
	response := handler.HandleRPC(RPCRequest{
		ID:           "auth.anonymous",
		Payload:      map[string]any{},
		PayloadError: "invalid JSON payload: unexpected end of JSON input",
	})
	if response.OK || response.Status != 400 || response.ErrorCode != CodeInvalidRequest || !strings.Contains(response.Message, "invalid JSON payload") {
		t.Fatalf("malformed binding payload should be rejected as invalid request, got %+v", response)
	}
}

func TestNakamaRPCMapsExternalSessionBeforeBusinessDispatch(t *testing.T) {
	handler := New(core.NewService(core.Config{}))
	response := handler.HandleRPC(RPCRequest{
		ID:          "bootstrap",
		SessionID:   "nakama-session-direct",
		UserID:      "nakama-user-direct",
		DisplayName: "Direct Nakama Player",
		Payload:     envelopePayload(1, "nonce-direct-bootstrap", "bootstrap", map[string]any{}),
	})
	if !response.OK || response.Status != 200 {
		t.Fatalf("expected external Nakama session bootstrap, got %+v", response)
	}
	bootstrap, ok := response.Payload.(*core.BootstrapSnapshot)
	if !ok {
		t.Fatalf("expected bootstrap payload, got %T", response.Payload)
	}
	if bootstrap.UserID != "nakama-user-direct" || bootstrap.SessionToken != "nakama-session-direct" || bootstrap.DisplayName != "Direct Nakama Player" {
		t.Fatalf("bootstrap should use external Nakama identity: %+v", bootstrap)
	}
}

func TestNakamaRPCMatchEntryRejectsIncompatibleClientVersion(t *testing.T) {
	handler := New(core.NewService(core.Config{}))
	aliceSession := "nakama-version-a-session"
	aliceUser := "nakama-version-a-user"
	hostSession := "nakama-version-host-session"
	hostUser := "nakama-version-host-user"
	guestSession := "nakama-version-guest-session"
	guestUser := "nakama-version-guest-user"

	queue := handler.HandleRPC(RPCRequest{
		ID:        "matchmaking.join",
		SessionID: aliceSession,
		UserID:    aliceUser,
		Payload: envelopePayload(1, "nonce-version-queue", "matchmaking_join", map[string]any{
			"mode_id":        "pvp_duel",
			"active_deck_id": "nakama_version_a_deck",
			"deck_snapshot":  deckPayload("nakama_version_a_deck"),
			"client_version": map[string]any{"protocol_version": core.ProtocolVersion + 1},
		}),
	})
	if queue.OK || queue.Status != 400 || queue.ErrorCode != "mode_invalid" {
		t.Fatalf("expected protocol mismatch rejection, got %+v", queue)
	}
	partialQueue := handler.HandleRPC(RPCRequest{
		ID:        "matchmaking.join",
		SessionID: aliceSession,
		UserID:    aliceUser,
		Payload: envelopePayload(2, "nonce-version-queue-partial", "matchmaking_join", map[string]any{
			"mode_id":        "pvp_duel",
			"active_deck_id": "nakama_version_a_deck",
			"deck_snapshot":  deckPayload("nakama_version_a_deck"),
			"client_version": map[string]any{"protocol_version": core.ProtocolVersion},
		}),
	})
	if partialQueue.OK || partialQueue.Status != 400 || partialQueue.ErrorCode != "mode_invalid" {
		t.Fatalf("expected partial version rejection, got %+v", partialQueue)
	}

	created := handler.HandleRPC(RPCRequest{
		ID:        "rooms.create",
		SessionID: hostSession,
		UserID:    hostUser,
		Payload: envelopePayload(1, "nonce-version-room-create", "rooms_create", map[string]any{
			"mode_id":        "pvp_duel",
			"active_deck_id": "nakama_version_host_deck",
			"deck_snapshot":  deckPayload("nakama_version_host_deck"),
			"client_version": map[string]any{
				"protocol_version":     core.ProtocolVersion,
				"business_api_version": core.BusinessAPIVersion,
				"battle_api_version":   core.BattleAPIVersion,
				"ruleset_version":      core.RulesetVersion,
			},
		}),
	})
	if !created.OK {
		t.Fatalf("compatible room create failed: %+v", created)
	}
	room := created.Payload.(*core.QueueResponse)

	joined := handler.HandleRPC(RPCRequest{
		ID:        "rooms.join",
		SessionID: guestSession,
		UserID:    guestUser,
		Payload: envelopePayload(1, "nonce-version-room-join", "rooms_join", map[string]any{
			"room_code":      room.RoomCode,
			"mode_id":        "pvp_duel",
			"active_deck_id": "nakama_version_guest_deck",
			"deck_snapshot":  deckPayload("nakama_version_guest_deck"),
			"client_version": map[string]any{"ruleset_version": "ruleset-old"},
		}),
	})
	if joined.OK || joined.Status != 400 || joined.ErrorCode != "mode_invalid" {
		t.Fatalf("expected ruleset mismatch rejection, got %+v", joined)
	}
}

func TestNakamaWSSUsesSharedEnvelopeGuardForPresenceHeartbeat(t *testing.T) {
	handler := New(core.NewService(core.Config{}))
	login := handler.HandleRPC(RPCRequest{
		ID:      "auth.anonymous",
		Payload: map[string]any{"device_id": "device-wss", "display_name": "WSS Player"},
	})
	if !login.OK {
		t.Fatalf("login failed: %+v", login)
	}
	session := login.Payload.(*core.AuthSession)

	heartbeat := handler.HandleWSSMessage(WSSMessage{
		Name:      "presence.heartbeat",
		SessionID: session.SessionToken,
		UserID:    session.UserID,
		Payload: envelopePayload(1, "nonce-wss-heartbeat", "presence_heartbeat", map[string]any{
			"client_state": "lobby",
		}),
	})
	if !heartbeat.OK || heartbeat.Status != 200 {
		t.Fatalf("expected heartbeat through wss envelope, got %+v", heartbeat)
	}
	if _, ok := heartbeat.Payload.(*core.PresenceHeartbeatResponse); !ok {
		t.Fatalf("expected heartbeat payload, got %T", heartbeat.Payload)
	}

	snapshot := handler.EnvelopeSnapshot()
	if snapshot.Accepted != 1 || len(snapshot.Audits) != 1 {
		t.Fatalf("unexpected envelope snapshot: %+v", snapshot)
	}
	if snapshot.Audits[0].Transport != security.BusinessEnvelopeTransportNakamaWSS || snapshot.Audits[0].Endpoint != "wss.presence.heartbeat" {
		t.Fatalf("expected wss audit metadata, got %+v", snapshot.Audits[0])
	}
}

func TestNakamaWSSRejectsStaleBusinessEnvelope(t *testing.T) {
	handler := New(core.NewService(core.Config{}))
	login := handler.HandleRPC(RPCRequest{
		ID:      "auth.anonymous",
		Payload: map[string]any{"device_id": "device-wss-stale", "display_name": "WSS Stale"},
	})
	if !login.OK {
		t.Fatalf("login failed: %+v", login)
	}
	session := login.Payload.(*core.AuthSession)

	stale := handler.HandleWSSMessage(WSSMessage{
		Name:      "presence.heartbeat",
		SessionID: session.SessionToken,
		UserID:    session.UserID,
		Payload: envelopePayloadAt(1, time.Now().Add(-10*time.Minute), "nonce-wss-stale", "presence_heartbeat", map[string]any{
			"client_tick": 1,
		}),
	})
	if stale.OK || stale.Status != 409 || stale.ErrorCode != security.CodeBusinessEnvelopeReplay {
		t.Fatalf("expected stale envelope replay rejection, got %+v", stale)
	}
	snapshot := handler.EnvelopeSnapshot()
	if snapshot.Rejected != 1 || snapshot.Audits[0].Reason != security.ReasonTimestampStale {
		t.Fatalf("expected stale audit, got %+v", snapshot)
	}
}

func TestNakamaRPCDispatchesBusinessMutationsWithEnvelopeBody(t *testing.T) {
	handler := New(core.NewService(core.Config{}))
	login := handler.HandleRPC(RPCRequest{
		ID:      "auth.anonymous",
		Payload: map[string]any{"device_id": "device-deck", "display_name": "Deck Player"},
	})
	if !login.OK {
		t.Fatalf("login failed: %+v", login)
	}
	session := login.Payload.(*core.AuthSession)

	save := handler.HandleRPC(RPCRequest{
		ID:        "decks.save",
		SessionID: session.SessionToken,
		UserID:    session.UserID,
		Payload: envelopePayload(1, "nonce-save-deck", "decks_save", map[string]any{
			"deck_id": "rpc_deck",
			"name":    "RPC Deck",
			"format":  "certification",
			"active":  true,
			"card_ids": []any{
				"focus_lens", "hitbox_charm", "density_surge", "tempo_break", "bomb_amplifier",
				"guard_seal", "graze_engine", "draw_sigil", "aim_baffle", "purge_charm",
				"focus_lens", "hitbox_charm", "density_surge", "tempo_break", "bomb_amplifier",
				"guard_seal", "graze_engine", "draw_sigil", "aim_baffle", "purge_charm",
			},
		}),
	})
	if !save.OK || save.Status != 200 {
		t.Fatalf("expected deck save through rpc envelope, got %+v", save)
	}
	if _, ok := save.Payload.(*core.SaveDeckResponse); !ok {
		t.Fatalf("expected save deck payload, got %T", save.Payload)
	}
}

func TestNakamaLobbyRPCAndWSSExposeRoomMVP(t *testing.T) {
	handler := New(core.NewService(core.Config{}))
	hostLogin := handler.HandleRPC(RPCRequest{
		ID:      "auth.anonymous",
		Payload: map[string]any{"device_id": "device-lobby-host", "display_name": "Lobby Host"},
	})
	if !hostLogin.OK {
		t.Fatalf("host login failed: %+v", hostLogin)
	}
	host := hostLogin.Payload.(*core.AuthSession)
	guestLogin := handler.HandleRPC(RPCRequest{
		ID:      "auth.anonymous",
		Payload: map[string]any{"device_id": "device-lobby-guest", "display_name": "Lobby Guest"},
	})
	if !guestLogin.OK {
		t.Fatalf("guest login failed: %+v", guestLogin)
	}
	guest := guestLogin.Payload.(*core.AuthSession)

	created := handler.HandleRPC(RPCRequest{
		ID:        "rooms.create",
		SessionID: host.SessionToken,
		UserID:    host.UserID,
		Payload: envelopePayload(1, "nonce-room-create", "rooms_create", map[string]any{
			"mode_id":        "world_boss",
			"active_deck_id": "host_lobby_deck",
			"deck_snapshot":  deckPayload("host_lobby_deck"),
			"mode_params":    map[string]any{"stage_id": "lunar_maze", "character_id": "precision"},
		}),
	})
	if !created.OK || created.Status != 200 {
		t.Fatalf("create room failed: %+v", created)
	}
	createPayload := created.Payload.(*core.QueueResponse)
	if createPayload.RoomCode == "" || createPayload.RequiredPlayers != 4 {
		t.Fatalf("create room payload invalid: %+v", createPayload)
	}

	list := handler.HandleRPC(RPCRequest{
		ID:        "rooms.list",
		SessionID: guest.SessionToken,
		UserID:    guest.UserID,
		Payload:   envelopePayload(1, "nonce-room-list", "rooms_list", map[string]any{}),
	})
	if !list.OK || list.Status != 200 {
		t.Fatalf("list rooms failed: %+v", list)
	}
	listPayload := list.Payload.(*core.RoomListResponse)
	if !listPayload.OK || len(listPayload.Rooms) != 1 || listPayload.Rooms[0].RoomCode != createPayload.RoomCode {
		t.Fatalf("list rooms payload invalid: %+v", listPayload)
	}

	rules := handler.HandleRPC(RPCRequest{
		ID:        "rooms.rules",
		SessionID: guest.SessionToken,
		UserID:    guest.UserID,
		Payload: envelopePayload(2, "nonce-room-rules", "rooms_rules", map[string]any{
			"room_code": createPayload.RoomCode,
		}),
	})
	if !rules.OK || rules.Status != 200 {
		t.Fatalf("room rules failed: %+v", rules)
	}
	rulesPayload := rules.Payload.(*core.RoomRulesSnapshot)
	if !rulesPayload.ServerAuthoritative || rulesPayload.Room.RoomCode != createPayload.RoomCode || rulesPayload.Version.ProtocolVersion != core.ProtocolVersion {
		t.Fatalf("room rules payload invalid: %+v", rulesPayload)
	}
	if len(rulesPayload.ForbiddenFields) == 0 || rulesPayload.Room.Participants[0].DeckSnapshotHash == "" {
		t.Fatalf("room rules missing protocol fields: %+v", rulesPayload)
	}
	if !rulesPayload.BusinessEnvelope || rulesPayload.ClientResultSubmit || rulesPayload.HighFrequencyBattleTickAllowed {
		t.Fatalf("room rules should require business envelope and forbid client result submit/high-frequency tick: %+v", rulesPayload)
	}
	if !stringSliceContains(rulesPayload.BusinessTransports, "nakama_wss") || !stringSliceContains(rulesPayload.BattleTransports, "kcp_udp") {
		t.Fatalf("room rules should expose transport split contract: %+v", rulesPayload)
	}
	if !stringSliceContains(rulesPayload.ClientOperations, "battle.servers") || !stringSliceContains(rulesPayload.ClientOperations, "battle.ticket") || !stringSliceContains(rulesPayload.ClientOperations, "matchmaking.cancel") || stringSliceContains(rulesPayload.ClientOperations, "battle.result.submit") || stringSliceContains(rulesPayload.ClientOperations, "battle.servers.register") {
		t.Fatalf("room rules should expose client RPC/WSS operations without result submit: %+v", rulesPayload)
	}
	for _, forbiddenOp := range []string{"match.input", "match.snapshot", "match.events", "match.settle", "battle.input", "battle.snapshot", "battle.events", "battle.result.submit"} {
		if !stringSliceContains(rulesPayload.DisallowedClientOperations, forbiddenOp) || stringSliceContains(rulesPayload.ClientOperations, forbiddenOp) || stringSliceContains(rulesPayload.ClientRPCOperations, forbiddenOp) || stringSliceContains(rulesPayload.ClientWSSOperations, forbiddenOp) {
			t.Fatalf("room rules should explicitly disallow Nakama client op %q: %+v", forbiddenOp, rulesPayload)
		}
	}
	if !stringSliceContains(rulesPayload.ClientRPCOperations, "battle.allocation") || !stringSliceContains(rulesPayload.ClientWSSOperations, "battle.ticket") || stringSliceContains(rulesPayload.ClientRPCOperations, "battle.result.submit") || stringSliceContains(rulesPayload.ClientWSSOperations, "battle.ticket.consume") {
		t.Fatalf("room rules should expose split client RPC/WSS operations without service callbacks: %+v", rulesPayload)
	}
	if !stringSliceContains(rulesPayload.ClientRPCOperations, "activity.claim") || stringSliceContains(rulesPayload.ClientWSSOperations, "activity.claim") {
		t.Fatalf("room rules should keep RPC-only activity claim out of WSS contract: %+v", rulesPayload)
	}
	if !stringSliceContains(rulesPayload.ClientOperations, "business.event") {
		t.Fatalf("room rules should expose business event WSS/RPC contract: %+v", rulesPayload)
	}
	if rulesPayload.ServiceCallbackContext["runtime_ctx_mode"] != "rpc" || rulesPayload.ServiceCallbackContext["gensoulkyo_service_origin"] != "battle_server" || rulesPayload.ServiceCallbackContext["gensoulkyo_battle_callback"] != "true" {
		t.Fatalf("room rules should expose service callback context requirements: %+v", rulesPayload.ServiceCallbackContext)
	}
	if !stringSliceContains(rulesPayload.ClientOperations, "rooms.chat") || !stringSliceContains(rulesPayload.ClientOperations, "rooms.announcement") {
		t.Fatalf("room rules should expose registered room message aliases: %+v", rulesPayload.ClientOperations)
	}
	if !stringSliceContains(rulesPayload.ServiceCallbacks, "battle.result.submit") || !stringSliceContains(rulesPayload.ServiceCallbacks, "battle.ticket.consume") {
		t.Fatalf("room rules should expose service-origin callback operations: %+v", rulesPayload)
	}
	if !stringSliceContains(rulesPayload.BusinessNotifications, "battle.allocation") || !stringSliceContains(rulesPayload.BusinessNotifications, "battle.ticket") || !stringSliceContains(rulesPayload.BusinessNotifications, "settlement") || stringSliceContains(rulesPayload.BusinessNotifications, "battle.result.submit") {
		t.Fatalf("room rules should expose low-frequency business WSS notifications only: %+v", rulesPayload)
	}

	battleServersMissingEnvelope := handler.HandleWSSMessage(WSSMessage{
		Name:      "battle.servers",
		SessionID: guest.SessionToken,
		UserID:    guest.UserID,
		Payload:   map[string]any{},
	})
	if battleServersMissingEnvelope.OK || battleServersMissingEnvelope.Status != 400 || battleServersMissingEnvelope.ErrorCode != CodeBusinessEnvelopeRequired {
		t.Fatalf("battle server WSS discovery must require envelope, got %+v", battleServersMissingEnvelope)
	}
	battleServers := handler.HandleWSSMessage(WSSMessage{
		Name:      "battle.servers",
		SessionID: guest.SessionToken,
		UserID:    guest.UserID,
		Payload:   envelopePayload(3, "nonce-room-battle-servers", "battle_servers", map[string]any{}),
	})
	if !battleServers.OK || battleServers.Status != 200 {
		t.Fatalf("battle server WSS discovery failed: %+v", battleServers)
	}
	serverList := battleServers.Payload.(*core.BattleServerListResponse)
	if !serverList.OK || len(serverList.Servers) == 0 || !serverList.ServerAuthoritative {
		t.Fatalf("battle server WSS discovery payload invalid: %+v", serverList)
	}

	joined := handler.HandleWSSMessage(WSSMessage{
		Name:      "rooms.join",
		SessionID: guest.SessionToken,
		UserID:    guest.UserID,
		Payload: envelopePayload(4, "nonce-room-join", "rooms_join", map[string]any{
			"room_code":      createPayload.RoomCode,
			"mode_id":        "world_boss",
			"active_deck_id": "guest_lobby_deck",
			"deck_snapshot":  deckPayload("guest_lobby_deck"),
		}),
	})
	if !joined.OK || joined.Status != 200 {
		t.Fatalf("join room failed: %+v", joined)
	}
	joinPayload := joined.Payload.(*core.QueueResponse)
	if joinPayload.RoomStatus != "waiting" || joinPayload.CurrentPlayers != 2 {
		t.Fatalf("join room payload invalid: %+v", joinPayload)
	}

	chat := handler.HandleWSSMessage(WSSMessage{
		Name:      "rooms.message",
		SessionID: guest.SessionToken,
		UserID:    guest.UserID,
		Payload: envelopePayload(5, "nonce-room-chat", "rooms_message", map[string]any{
			"room_code":  createPayload.RoomCode,
			"message_id": "guest-chat-rpc-1",
			"kind":       "chat",
			"text":       "ready when party fills",
			"metadata":   map[string]any{"client_locale": "en-US"},
		}),
	})
	if !chat.OK || chat.Status != 200 {
		t.Fatalf("room chat failed: %+v", chat)
	}
	chatPayload := chat.Payload.(*core.LobbyMessage)
	if chatPayload.Kind != "chat" || chatPayload.UserID != guest.UserID || chatPayload.Metadata["client_locale"] != "en-US" || !chatPayload.ServerAuthoritative {
		t.Fatalf("room chat payload invalid: %+v", chatPayload)
	}

	duplicateChat := handler.HandleWSSMessage(WSSMessage{
		Name:      "rooms.message",
		SessionID: guest.SessionToken,
		UserID:    guest.UserID,
		Payload: envelopePayload(6, "nonce-room-chat-duplicate", "rooms_message", map[string]any{
			"room_code":  createPayload.RoomCode,
			"message_id": "guest-chat-rpc-1",
			"kind":       "chat",
			"text":       "should not replace original",
		}),
	})
	if !duplicateChat.OK || !duplicateChat.Payload.(*core.LobbyMessage).Duplicate {
		t.Fatalf("expected idempotent duplicate chat response, got %+v", duplicateChat)
	}

	badAuthorityChat := handler.HandleWSSMessage(WSSMessage{
		Name:      "rooms.message",
		SessionID: guest.SessionToken,
		UserID:    guest.UserID,
		Payload: envelopePayload(7, "nonce-room-chat-authority", "rooms_message", map[string]any{
			"room_code":  createPayload.RoomCode,
			"message_id": "guest-chat-bad",
			"kind":       "chat",
			"text":       "bad",
			"metadata":   map[string]any{"score": 1000000},
		}),
	})
	if badAuthorityChat.OK || badAuthorityChat.Status != 403 || badAuthorityChat.ErrorCode != "forbidden_field" {
		t.Fatalf("expected forbidden metadata rejection, got %+v", badAuthorityChat)
	}

	announcement := handler.HandleRPC(RPCRequest{
		ID:        "rooms.announcement",
		SessionID: host.SessionToken,
		UserID:    host.UserID,
		Payload: envelopePayload(2, "nonce-room-announcement", "rooms_announcement", map[string]any{
			"room_code":  createPayload.RoomCode,
			"message_id": "host-announcement-rpc-1",
			"text":       "boss route locked",
			"metadata":   map[string]any{"cta": "ready"},
		}),
	})
	if !announcement.OK || announcement.Payload.(*core.LobbyMessage).Kind != "announcement" {
		t.Fatalf("host announcement failed: %+v", announcement)
	}

	left := handler.HandleWSSMessage(WSSMessage{
		Name:      "rooms.leave",
		SessionID: guest.SessionToken,
		UserID:    guest.UserID,
		Payload: envelopePayload(8, "nonce-room-leave", "rooms_leave", map[string]any{
			"room_code": createPayload.RoomCode,
		}),
	})
	if !left.OK || left.Status != 200 {
		t.Fatalf("leave room failed: %+v", left)
	}
	leavePayload := left.Payload.(*core.QueueResponse)
	if leavePayload.QueueStatus != "cancelled" || leavePayload.CurrentPlayers != 1 {
		t.Fatalf("leave room payload invalid: %+v", leavePayload)
	}

	forbidden := handler.HandleRPC(RPCRequest{
		ID:        "rooms.create",
		SessionID: guest.SessionToken,
		UserID:    guest.UserID,
		Payload: envelopePayload(9, "nonce-room-forbidden", "rooms_create", map[string]any{
			"mode_id":        "world_boss",
			"active_deck_id": "bad_lobby_deck",
			"deck_snapshot":  deckPayload("bad_lobby_deck"),
			"mode_params":    map[string]any{"score": 999999},
		}),
	})
	if forbidden.OK || forbidden.Status != 403 || forbidden.ErrorCode != "forbidden_field" {
		t.Fatalf("expected forbidden client authority rejection, got %+v", forbidden)
	}
}

func TestNakamaExternalRoomModeBindingAndReadyDispatch(t *testing.T) {
	handler := New(core.NewService(core.Config{}))
	hostSession := "nakama-room-host-session"
	hostUser := "nakama-room-host-user"
	guestSession := "nakama-room-guest-session"
	guestUser := "nakama-room-guest-user"

	created := handler.HandleRPC(RPCRequest{
		ID:          "rooms.create",
		SessionID:   hostSession,
		UserID:      hostUser,
		DisplayName: "External Host",
		Payload: envelopePayload(1, "nonce-external-room-create", "rooms_create", map[string]any{
			"mode_id":        "pvp_duel",
			"active_deck_id": "external_host_deck",
			"deck_snapshot":  deckPayload("external_host_deck"),
			"mode_params":    map[string]any{"stage_id": "clockwork_bloom", "character_id": "precision"},
		}),
	})
	if !created.OK || created.Status != 200 {
		t.Fatalf("external room create failed: %+v", created)
	}
	room := created.Payload.(*core.QueueResponse)

	mismatched := handler.HandleWSSMessage(WSSMessage{
		Name:        "rooms.join",
		SessionID:   guestSession,
		UserID:      guestUser,
		DisplayName: "External Guest",
		Payload: envelopePayload(1, "nonce-external-room-mismatch", "rooms_join", map[string]any{
			"room_code":      room.RoomCode,
			"mode_id":        "certification",
			"active_deck_id": "external_guest_deck_bad",
			"deck_snapshot":  deckPayload("external_guest_deck_bad"),
		}),
	})
	if mismatched.OK || mismatched.ErrorCode != "mode_invalid" {
		t.Fatalf("expected room mode binding rejection, got %+v", mismatched)
	}

	joined := handler.HandleWSSMessage(WSSMessage{
		Name:        "rooms.join",
		SessionID:   guestSession,
		UserID:      guestUser,
		DisplayName: "External Guest",
		Payload: envelopePayload(2, "nonce-external-room-join", "rooms_join", map[string]any{
			"room_code":      room.RoomCode,
			"mode_id":        "pvp_duel",
			"active_deck_id": "external_guest_deck",
			"deck_snapshot":  deckPayload("external_guest_deck"),
		}),
	})
	if !joined.OK || joined.Status != 200 {
		t.Fatalf("external room join failed: %+v", joined)
	}
	found := joined.Payload.(*core.QueueResponse)
	if found.MatchID == "" || found.ModeID != "pvp_duel" || found.RoomStatus != "found" {
		t.Fatalf("joined room should bind pvp duel match: %+v", found)
	}

	polledTicket := handler.HandleWSSMessage(WSSMessage{
		Name:      "matchmaking.ticket",
		SessionID: guestSession,
		UserID:    guestUser,
		Payload: envelopePayload(3, "nonce-external-guest-ticket-poll", "matchmaking_ticket", map[string]any{
			"ticket_id": found.TicketID,
		}),
	})
	if !polledTicket.OK || polledTicket.Status != 200 {
		t.Fatalf("room ticket WSS poll failed: %+v", polledTicket)
	}
	polled := polledTicket.Payload.(*core.QueueResponse)
	if polled.TicketID != found.TicketID || polled.MatchID != found.MatchID || polled.RoomStatus != "found" || polled.BattleAllocation == nil || polled.BattleTicket == nil {
		t.Fatalf("room ticket WSS poll should return match allocation and signed ticket: %+v", polled)
	}

	matchEvent := handler.HandleWSSMessage(WSSMessage{
		Name:      "business.event",
		SessionID: guestSession,
		UserID:    guestUser,
		Payload: envelopePayload(4, "nonce-external-guest-business-event", "business_event", map[string]any{
			"kind":      "matchmaking",
			"ticket_id": found.TicketID,
		}),
	})
	if !matchEvent.OK || matchEvent.Status != 200 {
		t.Fatalf("business event WSS read failed: %+v", matchEvent)
	}
	eventPayload := matchEvent.Payload.(*core.BusinessEvent)
	if eventPayload.Topic != "nakama_wss.business.matchmaking" || eventPayload.MatchID != found.MatchID || eventPayload.QueueStatus != "found" || eventPayload.BattleAllocation == nil || eventPayload.BattleTicket == nil {
		t.Fatalf("business event should include low-frequency matchmaking allocation/ticket state: %+v", eventPayload)
	}
	if eventPayload.Version.ProtocolVersion != core.ProtocolVersion || eventPayload.Version.RulesetVersion != core.RulesetVersion || eventPayload.Version.BusinessAPIVersion != core.BusinessAPIVersion || eventPayload.Version.BattleAPIVersion != core.BattleAPIVersion {
		t.Fatalf("business event should include shared protocol version stamp: %+v", eventPayload.Version)
	}
	if eventPayload.HighFrequencyBattleTickAllowed || eventPayload.ClientResultSubmitAllowed || stringSliceContains(eventPayload.AllowedClientOperations, "battle.result.submit") {
		t.Fatalf("business event must not authorize high-frequency tick or client result submit: %+v", eventPayload)
	}
	for _, forbiddenOp := range []string{"match.input", "match.snapshot", "match.events", "match.settle", "battle.input", "battle.snapshot", "battle.events", "battle.result.submit"} {
		if !stringSliceContains(eventPayload.DisallowedClientOperations, forbiddenOp) || stringSliceContains(eventPayload.AllowedClientOperations, forbiddenOp) || stringSliceContains(eventPayload.AllowedClientRPCOperations, forbiddenOp) || stringSliceContains(eventPayload.AllowedClientWSSOperations, forbiddenOp) {
			t.Fatalf("business event should explicitly disallow Nakama client op %q: %+v", forbiddenOp, eventPayload)
		}
	}
	if !eventPayload.BusinessEnvelopeRequired || !stringSliceContains(eventPayload.ForbiddenFields, "damage") || !stringSliceContains(eventPayload.ForbiddenFields, "settlement_key") {
		t.Fatalf("business event should expose envelope and forbidden-field contract: %+v", eventPayload)
	}
	if !stringSliceContains(eventPayload.AllowedClientOperations, "business.event") || !stringSliceContains(eventPayload.AllowedClientOperations, "rooms.chat") || !stringSliceContains(eventPayload.AllowedClientOperations, "rooms.announcement") || !stringSliceContains(eventPayload.AllowedClientOperations, "battle.servers") || stringSliceContains(eventPayload.AllowedClientOperations, "battle.servers.register") || !stringSliceContains(eventPayload.ServiceCallbacks, "battle.result.submit") {
		t.Fatalf("business event should document client operations and service callbacks: %+v", eventPayload)
	}
	if eventPayload.ServiceCallbackContext["runtime_ctx_mode"] != "rpc" || eventPayload.ServiceCallbackContext["gensoulkyo_service_origin"] != "battle_server" || eventPayload.ServiceCallbackContext["gensoulkyo_battle_callback"] != "true" {
		t.Fatalf("business event should document service callback context requirements: %+v", eventPayload.ServiceCallbackContext)
	}
	if !stringSliceContains(eventPayload.AllowedClientRPCOperations, "battle.allocation") || !stringSliceContains(eventPayload.AllowedClientWSSOperations, "battle.ticket") || stringSliceContains(eventPayload.AllowedClientRPCOperations, "battle.result.submit") || stringSliceContains(eventPayload.AllowedClientWSSOperations, "battle.ticket.consume") {
		t.Fatalf("business event should document split client RPC/WSS operations without service callbacks: %+v", eventPayload)
	}
	if !stringSliceContains(eventPayload.AllowedClientRPCOperations, "activity.claim") || stringSliceContains(eventPayload.AllowedClientWSSOperations, "activity.claim") {
		t.Fatalf("business event should keep RPC-only activity claim out of WSS contract: %+v", eventPayload)
	}
	if !stringSliceContains(eventPayload.BusinessNotifications, "matchmaking") || !stringSliceContains(eventPayload.BusinessNotifications, "battle.allocation") || !stringSliceContains(eventPayload.BusinessNotifications, "battle.ticket") || stringSliceContains(eventPayload.BusinessNotifications, "battle.result.submit") {
		t.Fatalf("business event should document low-frequency notification kinds only: %+v", eventPayload)
	}

	hostReady := handler.HandleRPC(RPCRequest{
		ID:        "match.ready",
		SessionID: hostSession,
		UserID:    hostUser,
		Payload: envelopePayload(2, "nonce-external-host-ready", "match_ready", map[string]any{
			"match_id": found.MatchID,
		}),
	})
	if !hostReady.OK || hostReady.Payload.(*core.ReadyResponse).ReadyStatus != "loading" {
		t.Fatalf("host ready invalid: %+v", hostReady)
	}
	guestReady := handler.HandleWSSMessage(WSSMessage{
		Name:      "match.ready",
		SessionID: guestSession,
		UserID:    guestUser,
		Payload: envelopePayload(5, "nonce-external-guest-ready", "match_ready", map[string]any{
			"match_id": found.MatchID,
		}),
	})
	if !guestReady.OK || guestReady.Payload.(*core.ReadyResponse).MatchStart == nil {
		t.Fatalf("guest ready should start match: %+v", guestReady)
	}
	start := guestReady.Payload.(*core.ReadyResponse).MatchStart
	if start.ModeID != "pvp_duel" || start.ModeRulesetVersion != "pvp-duel-s0" || start.BattleAllocation == nil {
		t.Fatalf("ready dispatch missing mode-bound allocation: %+v", start)
	}

	duplicateHostReady := handler.HandleRPC(RPCRequest{
		ID:        "match.ready",
		SessionID: hostSession,
		UserID:    hostUser,
		Payload: envelopePayload(3, "nonce-external-host-ready-duplicate", "match_ready", map[string]any{
			"match_id": found.MatchID,
		}),
	})
	if !duplicateHostReady.OK || duplicateHostReady.Payload.(*core.ReadyResponse).ReadyStatus != "running" || duplicateHostReady.Payload.(*core.ReadyResponse).ReadyCount != 2 {
		t.Fatalf("duplicate host ready should remain idempotent: %+v", duplicateHostReady)
	}
	heartbeat := handler.HandleWSSMessage(WSSMessage{
		Name:      "presence.heartbeat",
		SessionID: guestSession,
		UserID:    guestUser,
		Payload: envelopePayload(6, "nonce-external-guest-heartbeat", "presence_heartbeat", map[string]any{
			"match_id": found.MatchID,
		}),
	})
	if !heartbeat.OK || heartbeat.Payload.(*core.PresenceHeartbeatResponse).PresenceStatus != "in_match" || heartbeat.Payload.(*core.PresenceHeartbeatResponse).ModeID != "pvp_duel" {
		t.Fatalf("heartbeat should report authoritative running room match: %+v", heartbeat)
	}

	wssAllocation := handler.HandleWSSMessage(WSSMessage{
		Name:      "battle.allocation",
		SessionID: hostSession,
		UserID:    hostUser,
		Payload: envelopePayload(4, "nonce-external-host-allocation", "battle_allocation", map[string]any{
			"match_id": found.MatchID,
		}),
	})
	if !wssAllocation.OK || wssAllocation.Status != 200 {
		t.Fatalf("battle allocation WSS read failed: %+v", wssAllocation)
	}
	allocation := wssAllocation.Payload.(*core.BattleServerAllocation)
	if allocation.MatchID != found.MatchID || allocation.ModeID != "pvp_duel" || !allocation.ServerAuthoritative {
		t.Fatalf("battle allocation WSS payload should stay match-bound and authoritative: %+v", allocation)
	}

	wssTicket := handler.HandleWSSMessage(WSSMessage{
		Name:      "battle.ticket",
		SessionID: hostSession,
		UserID:    hostUser,
		Payload: envelopePayload(5, "nonce-external-host-ticket", "battle_ticket", map[string]any{
			"match_id": found.MatchID,
		}),
	})
	if !wssTicket.OK || wssTicket.Status != 200 {
		t.Fatalf("battle ticket WSS read failed: %+v", wssTicket)
	}
	ticket := wssTicket.Payload.(*core.SignedBattleTicket)
	if ticket.Ticket.MatchID != found.MatchID || ticket.Ticket.UserID != hostUser || ticket.Ticket.ModeID != "pvp_duel" || !ticket.Ticket.ServerAuthoritative || ticket.SignatureHex == "" {
		t.Fatalf("battle ticket WSS payload should be signed and user-bound: %+v", ticket)
	}

	auditStatus := handler.HandleRPC(RPCRequest{
		ID:        "battle.audit.status",
		SessionID: hostSession,
		UserID:    hostUser,
		Payload:   envelopePayload(6, "nonce-external-host-audit-status", "battle_audit_status", map[string]any{}),
	})
	if !auditStatus.OK || auditStatus.Status != 200 {
		t.Fatalf("battle audit status RPC read failed: %+v", auditStatus)
	}
	status := auditStatus.Payload.(core.BattleLifecycleAuditStatus)
	if status.Configured || status.OK || !status.ServerAuthoritative {
		t.Fatalf("default in-memory handler should surface missing durable audit repository: %+v", status)
	}

	lobbyAuditStatus := handler.HandleRPC(RPCRequest{
		ID:        "lobby.audit.status",
		SessionID: hostSession,
		UserID:    hostUser,
		Payload:   envelopePayload(7, "nonce-external-host-lobby-audit-status", "lobby_audit_status", map[string]any{}),
	})
	if !lobbyAuditStatus.OK || lobbyAuditStatus.Status != 200 {
		t.Fatalf("lobby audit status RPC read failed: %+v", lobbyAuditStatus)
	}
	lobbyStatus := lobbyAuditStatus.Payload.(core.LobbyLifecycleAuditStatus)
	if lobbyStatus.Configured || lobbyStatus.OK || !lobbyStatus.ServerAuthoritative {
		t.Fatalf("default in-memory handler should surface missing durable lobby audit repository: %+v", lobbyStatus)
	}
	if lobbyStatus.RoomReadRecords != 0 || lobbyStatus.LastSuccessOperation != "" {
		t.Fatalf("unconfigured lobby audit repository must not fake ticket-read progress: %+v", lobbyStatus)
	}

	wssResultSubmit := handler.HandleWSSMessage(WSSMessage{
		Name:      "battle.result.submit",
		SessionID: hostSession,
		UserID:    hostUser,
		Payload: envelopePayload(8, "nonce-external-host-result-submit", "battle_result_submit", map[string]any{
			"match_id": found.MatchID,
		}),
	})
	if wssResultSubmit.OK || wssResultSubmit.Status != 403 || wssResultSubmit.ErrorCode != CodeServiceOriginRequired {
		t.Fatalf("battle result submit must stay out of client WSS dispatch: %+v", wssResultSubmit)
	}
}

func TestNakamaReplayReadRequiresEnvelopeAndOwner(t *testing.T) {
	handler := New(core.NewService(core.Config{}))
	hostSession := "nakama-replay-host-session"
	hostUser := "nakama-replay-host-user"
	guestSession := "nakama-replay-guest-session"
	guestUser := "nakama-replay-guest-user"

	created := handler.HandleRPC(RPCRequest{
		ID:          "rooms.create",
		SessionID:   hostSession,
		UserID:      hostUser,
		DisplayName: "Replay Host",
		Payload: envelopePayload(1, "nonce-replay-room-create", "rooms_create", map[string]any{
			"mode_id":        "pvp_duel",
			"active_deck_id": "replay_host_deck",
			"deck_snapshot":  deckPayload("replay_host_deck"),
		}),
	})
	if !created.OK || created.Status != 200 {
		t.Fatalf("room create failed: %+v", created)
	}
	room := created.Payload.(*core.QueueResponse)

	joined := handler.HandleWSSMessage(WSSMessage{
		Name:        "rooms.join",
		SessionID:   guestSession,
		UserID:      guestUser,
		DisplayName: "Replay Guest",
		Payload: envelopePayload(1, "nonce-replay-room-join", "rooms_join", map[string]any{
			"room_code":      room.RoomCode,
			"mode_id":        "pvp_duel",
			"active_deck_id": "replay_guest_deck",
			"deck_snapshot":  deckPayload("replay_guest_deck"),
		}),
	})
	if !joined.OK || joined.Status != 200 {
		t.Fatalf("room join failed: %+v", joined)
	}
	match := joined.Payload.(*core.QueueResponse)
	playerIDs := []string{}
	for _, player := range match.BattleAllocation.Players {
		playerIDs = append(playerIDs, player.PlayerID)
	}

	resultSubmit := handler.HandleRPC(RPCRequest{
		ID:      "battle.result.submit",
		Service: true,
		Payload: map[string]any{"signed_result": map[string]any{
			"ok": true,
			"result": map[string]any{
				"version": map[string]any{
					"protocol_version":     core.ProtocolVersion,
					"business_api_version": core.BusinessAPIVersion,
					"battle_api_version":   core.BattleAPIVersion,
					"ruleset_version":      core.RulesetVersion,
				},
				"match_id":               match.MatchID,
				"mode_id":                match.ModeID,
				"result_hash":            "sha256:abcd1234",
				"replay_id":              "nakama-replay-read-callback",
				"player_ids":             playerIDs,
				"reward_projection_json": `{"source":"battle_server"}`,
				"mode_result_json":       `{"verified":true}`,
				"settled_at_ms":          time.Now().UnixMilli(),
			},
			"signature_alg":        "ED25519",
			"key_id":               match.BattleAllocation.BattleServerID,
			"signature_hex":        strings.Repeat("c", 128),
			"public_key_hex":       strings.Repeat("d", 64),
			"server_authoritative": true,
		}},
	})
	if !resultSubmit.OK {
		t.Fatalf("service-origin result submit failed: %+v", resultSubmit)
	}

	settlement := handler.HandleRPC(RPCRequest{
		ID:        "replay.get",
		SessionID: hostSession,
		UserID:    hostUser,
		Payload:   envelopePayload(2, "nonce-replay-get-missing-id", "replay_get", map[string]any{}),
	})
	if settlement.OK || settlement.Status != 400 || settlement.ErrorCode != "invalid_request" {
		t.Fatalf("missing replay id should be rejected after envelope validation: %+v", settlement)
	}

	hostMatchEnd := handler.HandleRPC(RPCRequest{
		ID:        "battle.audit.status",
		SessionID: hostSession,
		UserID:    hostUser,
		Payload:   envelopePayload(3, "nonce-replay-audit-status", "battle_audit_status", map[string]any{}),
	})
	if !hostMatchEnd.OK {
		t.Fatalf("audit status read failed: %+v", hostMatchEnd)
	}

	hostSettlement, err := handler.service.SettleMatch(hostSession, match.MatchID, map[string]any{})
	if err != nil {
		t.Fatalf("read host settlement after service-origin result: %v", err)
	}
	if !hostSettlement.Duplicate || hostSettlement.ReplayID == "" {
		t.Fatalf("expected duplicate authoritative settlement with replay id: %+v", hostSettlement)
	}

	read := handler.HandleRPC(RPCRequest{
		ID:        "replay.get",
		SessionID: hostSession,
		UserID:    hostUser,
		Payload: envelopePayload(4, "nonce-replay-owner-read", "replay_get", map[string]any{
			"replay_id": "replay_" + match.MatchID + "_",
		}),
	})
	if read.OK || read.Status != 404 {
		t.Fatalf("prefix replay ids must not be accepted: %+v", read)
	}

	ownerReplayID := hostSettlement.ReplayID
	guestRead := handler.HandleWSSMessage(WSSMessage{
		Name:      "replay.get",
		SessionID: guestSession,
		UserID:    guestUser,
		Payload: envelopePayload(2, "nonce-replay-guest-read", "replay_get", map[string]any{
			"replay_id": ownerReplayID,
		}),
	})
	if guestRead.OK || guestRead.Status != 401 || guestRead.ErrorCode != "unauthorized" {
		t.Fatalf("cross-user replay read should be rejected through WSS: %+v", guestRead)
	}

	ownerRead := handler.HandleRPC(RPCRequest{
		ID:        "replay.get",
		SessionID: hostSession,
		UserID:    hostUser,
		Payload: envelopePayload(5, "nonce-replay-owner-read-real", "replay_get", map[string]any{
			"replay_id": ownerReplayID,
		}),
	})
	if !ownerRead.OK || ownerRead.Status != 200 {
		t.Fatalf("owner replay read failed: %+v", ownerRead)
	}
	replay := ownerRead.Payload.(*core.ReplayRecord)
	if replay.ReplayID != ownerReplayID || replay.MatchID != match.MatchID || replay.UserID != hostUser || !replay.ServerAuthoritative || replay.ModeResult["battle_result_replay_id"] != "nakama-replay-read-callback" {
		t.Fatalf("owner replay payload invalid: %+v", replay)
	}
}

func TestNakamaRPCRejectsClientOriginBattleResultSubmit(t *testing.T) {
	handler := New(core.NewService(core.Config{}))
	login := handler.HandleRPC(RPCRequest{
		ID:      "auth.anonymous",
		Payload: map[string]any{"device_id": "device-result-client", "display_name": "Result Client"},
	})
	if !login.OK {
		t.Fatalf("login failed: %+v", login)
	}
	session := login.Payload.(*core.AuthSession)

	clientSubmit := handler.HandleRPC(RPCRequest{
		ID:        "battle.result.submit",
		SessionID: session.SessionToken,
		UserID:    session.UserID,
		Payload: envelopePayload(1, "nonce-client-result-submit", "battle_result_submit", map[string]any{
			"signed_result": map[string]any{"match_id": "client-authored"},
		}),
	})
	if clientSubmit.OK || clientSubmit.Status != 403 || clientSubmit.ErrorCode != CodeServiceOriginRequired {
		t.Fatalf("client-origin battle result submit must be rejected before core dispatch: %+v", clientSubmit)
	}
	snapshot := handler.EnvelopeSnapshot()
	if snapshot.Accepted != 0 || snapshot.Rejected != 1 || snapshot.SessionCount != 0 || len(snapshot.Audits) != 1 {
		t.Fatalf("service-origin rejection should audit without accepted replay state: %+v", snapshot)
	}
	audit := snapshot.Audits[0]
	if audit.Accepted || audit.Transport != security.BusinessEnvelopeTransportNakamaRPC || audit.Endpoint != "rpc.battle.result.submit" || audit.Reason != security.ReasonVersion || audit.UserID != session.UserID || audit.SessionIDHint == session.SessionToken {
		t.Fatalf("service-origin RPC rejection audit should be non-secret and synthetic: %+v", audit)
	}

	bootstrap := handler.HandleRPC(RPCRequest{
		ID:        "bootstrap",
		SessionID: session.SessionToken,
		UserID:    session.UserID,
		Payload:   envelopePayload(1, "nonce-client-result-submit", "bootstrap", map[string]any{}),
	})
	if !bootstrap.OK || bootstrap.Status != 200 {
		t.Fatalf("client service-origin rejection must not consume the original envelope seq/nonce, got %+v", bootstrap)
	}
	if snapshot := handler.EnvelopeSnapshot(); snapshot.Accepted != 1 || snapshot.Rejected != 1 {
		t.Fatalf("follow-up bootstrap should consume the original client seq/nonce exactly once: %+v", snapshot)
	}
}

func TestNakamaBusinessEventSettlementAliasRejectsConflictingKind(t *testing.T) {
	handler := New(core.NewService(core.Config{}))
	response := handler.HandleWSSMessage(WSSMessage{
		Name:      "business.event.settlement",
		SessionID: "nakama-settlement-alias-session",
		UserID:    "nakama-settlement-alias-user",
		Payload: envelopePayload(1, "nonce-settlement-alias-kind-conflict", "business_event_settlement", map[string]any{
			"kind":     "matchmaking",
			"match_id": "match-conflict",
		}),
	})
	if response.OK || response.Status != 400 || response.ErrorCode != CodeInvalidRequest || !strings.Contains(response.Message, "requires settlement kind") {
		t.Fatalf("settlement alias should reject conflicting business event kind, got %+v", response)
	}
}

func TestNakamaRPCRejectsServiceOriginBattleResultSubmitWithPlayerContext(t *testing.T) {
	handler := New(core.NewService(core.Config{}))
	response := handler.HandleRPC(RPCRequest{
		ID:        "battle.result.submit",
		Service:   true,
		SessionID: "player-session",
		UserID:    "player-user",
		Payload: map[string]any{
			"signed_result": map[string]any{"match_id": "player-context"},
		},
	})
	if response.OK || response.Status != 403 || response.ErrorCode != CodeServiceOriginRequired || !strings.Contains(response.Message, "must not include player session context") {
		t.Fatalf("service-origin result submit with player context must fail closed before core dispatch, got %+v", response)
	}
	snapshot := handler.EnvelopeSnapshot()
	if snapshot.Accepted != 0 || snapshot.Rejected != 1 || snapshot.SessionCount != 0 || len(snapshot.Audits) != 1 {
		t.Fatalf("miswired service-origin player context should audit without replay state: %+v", snapshot)
	}
	audit := snapshot.Audits[0]
	if audit.Accepted || audit.Transport != security.BusinessEnvelopeTransportNakamaRPC || audit.Endpoint != "rpc.battle.result.submit" || audit.UserID != "player-user" || audit.SessionIDHint == "player-session" {
		t.Fatalf("miswired service-origin player context audit should be synthetic and sanitized: %+v", audit)
	}
}

func TestNakamaRPCAllowsServiceOriginBattleResultSubmitToReachCoreValidation(t *testing.T) {
	handler := New(core.NewService(core.Config{}))
	serviceSubmit := handler.HandleRPC(RPCRequest{
		ID:      "battle.result.submit",
		Service: true,
		Payload: map[string]any{
			"signed_result": map[string]any{"match_id": "service-invalid"},
		},
	})
	if serviceSubmit.OK || serviceSubmit.Status != 400 || serviceSubmit.ErrorCode != "invalid_request" || strings.Contains(serviceSubmit.Message, CodeServiceOriginRequired) {
		t.Fatalf("service-origin battle result submit should reach core validation, got %+v", serviceSubmit)
	}
}

func TestNakamaServiceOriginRPCRejectsBusinessEnvelopePayloadShape(t *testing.T) {
	handler := New(core.NewService(core.Config{}))
	callbacks := []struct {
		id   string
		op   string
		body map[string]any
	}{
		{
			id: "battle.servers.register",
			op: "battle_servers_register",
			body: map[string]any{
				"battle_server_id": "register-client-shaped",
				"endpoint":         "127.0.0.1:7999",
				"capacity":         4,
			},
		},
		{
			id: "battle.servers.heartbeat",
			op: "battle_servers_heartbeat",
			body: map[string]any{
				"battle_server_id": "heartbeat-client-shaped",
				"active_matches":   1,
				"load":             0.25,
			},
		},
		{
			id: "battle.servers.offline",
			op: "battle_servers_offline",
			body: map[string]any{
				"battle_server_id": "offline-client-shaped",
			},
		},
		{
			id: "battle.ticket.consume",
			op: "battle_ticket_consume",
			body: map[string]any{
				"version":          map[string]any{"protocol_version": core.ProtocolVersion, "business_api_version": core.BusinessAPIVersion, "battle_api_version": core.BattleAPIVersion, "ruleset_version": core.RulesetVersion},
				"ticket_id":        "ticket-client-shaped",
				"match_id":         "match-client-shaped",
				"battle_server_id": "battle-client-shaped",
				"ticket_nonce_hex": "nonce-client-shaped",
			},
		},
		{
			id: "battle.result.submit",
			op: "battle_result_submit",
			body: map[string]any{
				"signed_result": map[string]any{"match_id": "result-client-shaped"},
			},
		},
	}
	for index, callback := range callbacks {
		response := handler.HandleRPC(RPCRequest{
			ID:      callback.id,
			Service: true,
			Payload: envelopePayload(int64(index+1), "nonce-service-origin-envelope-"+callback.id, callback.op, callback.body),
		})
		if response.OK || response.Status != 400 || response.ErrorCode != CodeInvalidRequest || !strings.Contains(response.Message, "must not include business envelope") {
			t.Fatalf("service-origin callback %s should reject envelope-shaped payload before core dispatch: %+v", callback.id, response)
		}
	}
	snapshot := handler.EnvelopeSnapshot()
	rejectedCallbacks := int64(len(callbacks))
	if snapshot.Accepted != 0 || snapshot.Rejected != rejectedCallbacks || snapshot.SessionCount != 0 || len(snapshot.Audits) != len(callbacks) {
		t.Fatalf("service-origin envelope-shape rejection should audit without accepted replay state: %+v", snapshot)
	}
	for _, audit := range snapshot.Audits {
		if audit.Accepted || audit.Transport != security.BusinessEnvelopeTransportNakamaRPC || !strings.HasPrefix(audit.Endpoint, "rpc.") || audit.Reason != security.ReasonVersion || audit.SessionIDHint == "nonce-service-origin-envelope" {
			t.Fatalf("service-origin envelope-shape rejection audit should be synthetic and sanitized: %+v", audit)
		}
	}

	login := handler.HandleRPC(RPCRequest{
		ID:      "auth.anonymous",
		Payload: map[string]any{"device_id": "device-service-envelope-replay", "display_name": "Service Envelope Replay"},
	})
	if !login.OK {
		t.Fatalf("login failed: %+v", login)
	}
	session := login.Payload.(*core.AuthSession)
	bootstrap := handler.HandleRPC(RPCRequest{
		ID:        "bootstrap",
		SessionID: session.SessionToken,
		UserID:    session.UserID,
		Payload:   envelopePayload(1, "nonce-service-origin-envelope-battle.result.submit", "bootstrap", map[string]any{}),
	})
	if !bootstrap.OK || bootstrap.Status != 200 {
		t.Fatalf("service-origin envelope-shape rejection must not consume the client seq/nonce, got %+v", bootstrap)
	}
	if snapshot := handler.EnvelopeSnapshot(); snapshot.Accepted != 1 || snapshot.Rejected != rejectedCallbacks {
		t.Fatalf("follow-up bootstrap should consume the original client seq/nonce exactly once: %+v", snapshot)
	}
}

func TestNakamaServiceOriginRPCRejectsNestedBusinessEnvelopePayloadShape(t *testing.T) {
	handler := New(core.NewService(core.Config{}))
	for index, wrapper := range []string{"body", "request", "data"} {
		response := handler.HandleRPC(RPCRequest{
			ID:      "battle.result.submit",
			Service: true,
			Payload: map[string]any{
				wrapper: envelopePayload(int64(index+1), "nonce-service-origin-nested-envelope-"+wrapper, "battle_result_submit", map[string]any{
					"signed_result": map[string]any{"match_id": "nested-client-shaped"},
				}),
			},
		})
		if response.OK || response.Status != 400 || response.ErrorCode != CodeInvalidRequest || !strings.Contains(response.Message, "must not include business envelope") {
			t.Fatalf("service-origin callback with %s wrapper should reject nested envelope before core dispatch: %+v", wrapper, response)
		}
	}
	if snapshot := handler.EnvelopeSnapshot(); snapshot.Accepted != 0 || snapshot.Rejected != 3 || snapshot.SessionCount != 0 || len(snapshot.Audits) != 3 {
		t.Fatalf("nested service-origin envelope-shape rejection should audit without accepted replay state: %+v", snapshot)
	}
}

func TestNakamaServiceOriginRPCRejectsDirectBusinessEnvelopeFields(t *testing.T) {
	handler := New(core.NewService(core.Config{}))
	for index, payload := range []map[string]any{
		{
			"version":       security.BusinessEnvelopeVersion,
			"signed_result": map[string]any{"match_id": "direct-version-client-shaped"},
		},
		{
			"seq":           1,
			"timestamp_ms":  time.Now().UnixMilli(),
			"nonce":         "direct-envelope-fields",
			"op":            "battle_result_submit",
			"auth_tag":      strings.Repeat("1", 64),
			"signed_result": map[string]any{"match_id": "direct-client-shaped"},
		},
		{
			"body": map[string]any{
				"seq":           2,
				"timestamp_ms":  time.Now().UnixMilli(),
				"nonce":         "nested-direct-envelope-fields",
				"op":            "battle_result_submit",
				"auth_tag":      strings.Repeat("2", 64),
				"signed_result": map[string]any{"match_id": "nested-direct-client-shaped"},
			},
		},
		{
			"key_id":        "client-dev-key",
			"signed_result": map[string]any{"match_id": "direct-key-id-client-shaped"},
		},
		{
			"body": map[string]any{
				"keyID":         "client-dev-key",
				"signed_result": map[string]any{"match_id": "nested-key-id-client-shaped"},
			},
		},
		{
			"keyId":         "client-dev-key",
			"signed_result": map[string]any{"match_id": "camel-key-id-client-shaped"},
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
	} {
		response := handler.HandleRPC(RPCRequest{
			ID:      "battle.result.submit",
			Service: true,
			Payload: payload,
		})
		if response.OK || response.Status != 400 || response.ErrorCode != CodeInvalidRequest || !strings.Contains(response.Message, "must not include business envelope") {
			t.Fatalf("service-origin callback payload %d should reject direct envelope fields before core dispatch: %+v", index, response)
		}
	}
	if snapshot := handler.EnvelopeSnapshot(); snapshot.Accepted != 0 || snapshot.Rejected != 8 || snapshot.SessionCount != 0 || len(snapshot.Audits) != 8 {
		t.Fatalf("direct service-origin envelope-field rejection should audit without accepted replay state: %+v", snapshot)
	}
}

func TestNakamaWSSRejectsServiceOriginOnlyCallbacksBeforeReplayState(t *testing.T) {
	handler := New(core.NewService(core.Config{}))
	callbacks := core.ServiceCallbackOperations()
	for index, name := range callbacks {
		response := handler.HandleWSSMessage(WSSMessage{
			Name:      name,
			SessionID: "player-session",
			UserID:    "player-user",
			Payload: envelopePayload(int64(index+1), "nonce-wss-service-only-"+name, strings.ReplaceAll(name, ".", "_"), map[string]any{
				"match_id":         "client-authored",
				"battle_server_id": "client-battle-server",
			}),
		})
		if response.OK || response.Status != 403 || response.ErrorCode != CodeServiceOriginRequired {
			t.Fatalf("WSS %s should fail as service-origin RPC only before dispatch, got %+v", name, response)
		}
	}
	snapshot := handler.EnvelopeSnapshot()
	if snapshot.Accepted != 0 || snapshot.Rejected != int64(len(callbacks)) || snapshot.SessionCount != 0 {
		t.Fatalf("service-origin WSS rejections should be rejected audits without accepted replay state: %+v", snapshot)
	}
	for _, audit := range snapshot.Audits {
		if audit.Accepted || audit.Transport != security.BusinessEnvelopeTransportNakamaWSS || audit.Reason != security.ReasonVersion || !strings.HasPrefix(audit.Endpoint, "wss.battle.") || audit.SessionIDHint == "player-session" || audit.UserID != "player-user" {
			t.Fatalf("service-origin WSS rejection audit should be non-secret and synthetic: %+v", audit)
		}
	}
}

func TestNakamaClientDispatchRejectsHighFrequencyAndSettlementAuthorityOperations(t *testing.T) {
	handler := New(core.NewService(core.Config{}))
	sessionID := "nakama-disallowed-client-session"
	userID := "nakama-disallowed-client-user"
	for index, name := range []string{
		"match.input",
		"match.snapshot",
		"match.events",
		"match.settle",
		"battle.input",
		"battle.snapshot",
		"battle.events",
	} {
		rpc := handler.HandleRPC(RPCRequest{
			ID:        name,
			SessionID: sessionID,
			UserID:    userID,
			Payload:   envelopePayload(int64(index*2+1), "nonce-disallowed-rpc-"+name, strings.ReplaceAll(name, ".", "_"), map[string]any{"match_id": "client-match"}),
		})
		if rpc.OK || rpc.Status != 404 || rpc.ErrorCode != "not_found" {
			t.Fatalf("Nakama RPC %s must not expose high-frequency/result authority path, got %+v", name, rpc)
		}
		wss := handler.HandleWSSMessage(WSSMessage{
			Name:      name,
			SessionID: sessionID,
			UserID:    userID,
			Payload:   envelopePayload(int64(index*2+2), "nonce-disallowed-wss-"+name, strings.ReplaceAll(name, ".", "_"), map[string]any{"match_id": "client-match"}),
		})
		if wss.OK || wss.Status != 404 || wss.ErrorCode != "not_found" {
			t.Fatalf("Nakama WSS %s must not expose high-frequency/result authority path, got %+v", name, wss)
		}
	}
	for index, name := range []string{"battle.result.submit", "battle.ticket.consume", "battle.servers.register", "battle.servers.heartbeat", "battle.servers.offline"} {
		rpc := handler.HandleRPC(RPCRequest{
			ID:        name,
			SessionID: sessionID,
			UserID:    userID,
			Payload:   envelopePayload(int64(100+index*2), "nonce-disallowed-rpc-"+name, strings.ReplaceAll(name, ".", "_"), map[string]any{"match_id": "client-match"}),
		})
		if rpc.OK || rpc.Status != 403 || rpc.ErrorCode != CodeServiceOriginRequired {
			t.Fatalf("Nakama RPC %s must require service origin for callback authority, got %+v", name, rpc)
		}
		wss := handler.HandleWSSMessage(WSSMessage{
			Name:      name,
			SessionID: sessionID,
			UserID:    userID,
			Payload:   envelopePayload(int64(101+index*2), "nonce-disallowed-wss-"+name, strings.ReplaceAll(name, ".", "_"), map[string]any{"match_id": "client-match"}),
		})
		if wss.OK || wss.Status != 403 || wss.ErrorCode != CodeServiceOriginRequired {
			t.Fatalf("Nakama WSS %s must stay service-origin-only, got %+v", name, wss)
		}
	}
}

func TestNakamaBattleServerRegisterHeartbeatAndOfflineRequireServiceOrigin(t *testing.T) {
	handler := New(core.NewService(core.Config{}))
	login := handler.HandleRPC(RPCRequest{
		ID:      "auth.anonymous",
		Payload: map[string]any{"device_id": "device-battle-register", "display_name": "Battle Register Client"},
	})
	if !login.OK {
		t.Fatalf("login failed: %+v", login)
	}
	session := login.Payload.(*core.AuthSession)

	publicRegister := handler.HandleRPC(RPCRequest{
		ID:        "battle.servers.register",
		SessionID: session.SessionToken,
		UserID:    session.UserID,
		Payload: envelopePayload(1, "nonce-public-battle-register", "battle_servers_register", map[string]any{
			"battle_server_id": "public-battle-server",
			"endpoint":         "127.0.0.1:7999",
			"capacity":         8,
			"status":           "online",
		}),
	})
	if publicRegister.OK || publicRegister.Status != 403 || publicRegister.ErrorCode != CodeServiceOriginRequired {
		t.Fatalf("public battle server registration must require service origin, got %+v", publicRegister)
	}
	publicOffline := handler.HandleRPC(RPCRequest{
		ID:        "battle.servers.offline",
		SessionID: session.SessionToken,
		UserID:    session.UserID,
		Payload: envelopePayload(2, "nonce-public-battle-offline", "battle_servers_offline", map[string]any{
			"battle_server_id": "public-battle-server",
		}),
	})
	if publicOffline.OK || publicOffline.Status != 403 || publicOffline.ErrorCode != CodeServiceOriginRequired {
		t.Fatalf("public battle server offline must require service origin, got %+v", publicOffline)
	}

	miswiredRegister := handler.HandleRPC(RPCRequest{
		ID:        "battle.servers.register",
		Service:   true,
		SessionID: session.SessionToken,
		UserID:    session.UserID,
		Payload:   map[string]any{"battle_server_id": "miswired-battle-server", "endpoint": "127.0.0.1:7998", "capacity": 8},
	})
	if miswiredRegister.OK || miswiredRegister.Status != 403 || miswiredRegister.ErrorCode != CodeServiceOriginRequired {
		t.Fatalf("service-origin battle server registration must not include player context, got %+v", miswiredRegister)
	}
	miswiredOffline := handler.HandleRPC(RPCRequest{
		ID:        "battle.servers.offline",
		Service:   true,
		SessionID: session.SessionToken,
		UserID:    session.UserID,
		Payload:   map[string]any{"battle_server_id": "miswired-battle-server"},
	})
	if miswiredOffline.OK || miswiredOffline.Status != 403 || miswiredOffline.ErrorCode != CodeServiceOriginRequired {
		t.Fatalf("service-origin battle server offline must not include player context, got %+v", miswiredOffline)
	}

	registered := handler.HandleRPC(RPCRequest{
		ID:      "battle.servers.register",
		Service: true,
		Payload: map[string]any{
			"battle_server_id": "nakama-service-battle",
			"endpoint":         "127.0.0.1:7997",
			"region":           "local",
			"build_id":         "nakama-service-test",
			"capacity":         4,
			"active_matches":   1,
			"load":             0.25,
			"status":           "offline",
			"supported_modes":  []any{"pvp_duel"},
		},
	})
	if !registered.OK || registered.Status != 200 {
		t.Fatalf("service-origin battle server registration failed: %+v", registered)
	}
	status := registered.Payload.(*core.BattleServerStatus)
	if status.BattleServerID != "nakama-service-battle" || status.Endpoint != "127.0.0.1:7997" || status.ActiveMatches != 1 || status.Load != 0.25 || status.Status != "online" || len(status.SupportedModes) != 1 || status.SupportedModes[0] != "pvp_duel" || !status.ServerAuthoritative {
		t.Fatalf("registered status should canonicalize callback payload status to online: %+v", status)
	}

	heartbeat := handler.HandleRPC(RPCRequest{
		ID:      "battle.servers.heartbeat",
		Service: true,
		Payload: map[string]any{
			"battle_server_id": "nakama-service-battle",
			"active_matches":   2,
			"load":             0.5,
			"status":           "draining",
		},
	})
	if !heartbeat.OK || heartbeat.Status != 200 {
		t.Fatalf("service-origin battle server heartbeat failed: %+v", heartbeat)
	}
	heartbeatStatus := heartbeat.Payload.(*core.BattleServerStatus)
	if heartbeatStatus.Endpoint != "127.0.0.1:7997" || heartbeatStatus.Region != "local" || heartbeatStatus.BuildID != "nakama-service-test" || heartbeatStatus.ActiveMatches != 2 || heartbeatStatus.Load != 0.5 || heartbeatStatus.Status != "online" || !heartbeatStatus.ServerAuthoritative {
		t.Fatalf("heartbeat should preserve server metadata while canonicalizing payload status to online: %+v", heartbeatStatus)
	}
	offline := handler.HandleRPC(RPCRequest{
		ID:      "battle.servers.offline",
		Service: true,
		Payload: map[string]any{
			"battle_server_id": "nakama-service-battle",
			"status":           "online",
		},
	})
	if !offline.OK || offline.Status != 200 {
		t.Fatalf("service-origin battle server offline failed: %+v", offline)
	}
	offlineStatus := offline.Payload.(*core.BattleServerStatus)
	if offlineStatus.Status != "offline" || offlineStatus.Load != 0 || offlineStatus.Endpoint != "127.0.0.1:7997" || !offlineStatus.ServerAuthoritative {
		t.Fatalf("offline should ignore payload status, preserve server metadata, and mark unavailable: %+v", offlineStatus)
	}
	audit := handler.HandleRPC(RPCRequest{
		ID:        "battle.audit.status",
		SessionID: session.SessionToken,
		UserID:    session.UserID,
		Payload:   envelopePayload(3, "nonce-battle-register-audit", "battle_audit_status", map[string]any{}),
	})
	if !audit.OK {
		t.Fatalf("battle audit status failed: %+v", audit)
	}
	auditStatus := audit.Payload.(core.BattleLifecycleAuditStatus)
	if auditStatus.Configured || auditStatus.ServerLifecycleRecords != 0 || !auditStatus.ServerAuthoritative {
		t.Fatalf("unconfigured handler should not fake durable lifecycle audit writes: %+v", auditStatus)
	}

	servers := handler.HandleRPC(RPCRequest{
		ID:        "battle.servers",
		SessionID: session.SessionToken,
		UserID:    session.UserID,
		Payload:   envelopePayload(4, "nonce-public-battle-list", "battle_servers", map[string]any{}),
	})
	if !servers.OK || servers.Status != 200 {
		t.Fatalf("public battle server list failed: %+v", servers)
	}
	list := servers.Payload.(*core.BattleServerListResponse)
	found := false
	for _, server := range list.Servers {
		if server.BattleServerID == "nakama-service-battle" && server.ActiveMatches == 2 && server.Load == 0 && server.Status == "offline" {
			found = true
		}
	}
	if !found {
		t.Fatalf("service registered battle server missing from discovery: %+v", list)
	}
}

func TestNakamaHandlerDatabaseWiringRecordsEnvelopeLobbyAndBattleAudits(t *testing.T) {
	driverName := registerNakamaSQLCaptureDriver(t)
	db, err := sql.Open(driverName, "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	handler, err := NewWithDatabase(db)
	if err != nil {
		t.Fatal(err)
	}
	registered := handler.HandleRPC(RPCRequest{
		ID:      "battle.servers.register",
		Service: true,
		Payload: map[string]any{
			"battle_server_id": "nakama-sql-battle",
			"endpoint":         "127.0.0.1:7907",
			"region":           "local",
			"build_id":         "nakama-sql-test",
			"capacity":         4,
			"active_matches":   0,
			"load":             0,
			"status":           "online",
			"supported_modes":  []any{"pvp_duel"},
		},
	})
	if !registered.OK {
		t.Fatalf("service-origin battle server registration failed: %+v", registered)
	}
	heartbeat := handler.HandleRPC(RPCRequest{
		ID:      "battle.servers.heartbeat",
		Service: true,
		Payload: map[string]any{
			"battle_server_id": "nakama-sql-battle",
			"active_matches":   0,
			"load":             0,
			"status":           "online",
		},
	})
	if !heartbeat.OK {
		t.Fatalf("service-origin battle server heartbeat failed: %+v", heartbeat)
	}
	offline := handler.HandleRPC(RPCRequest{
		ID:      "battle.servers.offline",
		Service: true,
		Payload: map[string]any{
			"battle_server_id": "nakama-sql-battle",
		},
	})
	if !offline.OK {
		t.Fatalf("service-origin battle server offline failed: %+v", offline)
	}
	registeredAgain := handler.HandleRPC(RPCRequest{
		ID:      "battle.servers.register",
		Service: true,
		Payload: map[string]any{
			"battle_server_id": "nakama-sql-battle-active",
			"endpoint":         "127.0.0.1:7908",
			"region":           "local",
			"build_id":         "nakama-sql-active",
			"capacity":         4,
			"active_matches":   0,
			"load":             0,
			"status":           "online",
			"supported_modes":  []any{"pvp_duel"},
		},
	})
	if !registeredAgain.OK {
		t.Fatalf("service-origin replacement battle server registration failed: %+v", registeredAgain)
	}
	hostLogin := handler.HandleRPC(RPCRequest{
		ID:      "auth.anonymous",
		Payload: map[string]any{"device_id": "device-sql-host", "display_name": "SQL Host"},
	})
	if !hostLogin.OK {
		t.Fatalf("host login failed: %+v", hostLogin)
	}
	host := hostLogin.Payload.(*core.AuthSession)
	guestLogin := handler.HandleRPC(RPCRequest{
		ID:      "auth.anonymous",
		Payload: map[string]any{"device_id": "device-sql-guest", "display_name": "SQL Guest"},
	})
	if !guestLogin.OK {
		t.Fatalf("guest login failed: %+v", guestLogin)
	}
	guest := guestLogin.Payload.(*core.AuthSession)

	created := handler.HandleRPC(RPCRequest{
		ID:        "rooms.create",
		SessionID: host.SessionToken,
		UserID:    host.UserID,
		Payload: envelopePayload(1, "nonce-sql-room-create", "rooms_create", map[string]any{
			"mode_id":        "pvp_duel",
			"active_deck_id": "sql_host_deck",
			"deck_snapshot":  deckPayload("sql_host_deck"),
			"mode_params":    map[string]any{"stage_id": "starlit_lanes", "character_id": "balanced"},
		}),
	})
	if !created.OK {
		t.Fatalf("room create failed: %+v", created)
	}
	room := created.Payload.(*core.QueueResponse)

	snapshot := handler.HandleRPC(RPCRequest{
		ID:        "rooms.get",
		SessionID: guest.SessionToken,
		UserID:    guest.UserID,
		Payload: envelopePayload(2, "nonce-sql-room-get", "rooms_get", map[string]any{
			"room_code": room.RoomCode,
		}),
	})
	if !snapshot.OK {
		t.Fatalf("room snapshot read failed: %+v", snapshot)
	}

	message := handler.HandleWSSMessage(WSSMessage{
		Name:      "rooms.message",
		SessionID: host.SessionToken,
		UserID:    host.UserID,
		Payload: envelopePayload(2, "nonce-sql-room-message", "rooms_message", map[string]any{
			"room_code":  room.RoomCode,
			"message_id": "sql-host-message-1",
			"kind":       "chat",
			"text":       "waiting for duel",
			"metadata":   map[string]any{"client_locale": "en-US"},
		}),
	})
	if !message.OK {
		t.Fatalf("room message failed: %+v", message)
	}
	duplicateMessage := handler.HandleWSSMessage(WSSMessage{
		Name:      "rooms.message",
		SessionID: host.SessionToken,
		UserID:    host.UserID,
		Payload: envelopePayload(3, "nonce-sql-room-message-duplicate", "rooms_message", map[string]any{
			"room_code":  room.RoomCode,
			"message_id": "sql-host-message-1",
			"kind":       "chat",
			"text":       "duplicate should audit without replacing",
		}),
	})
	if !duplicateMessage.OK || !duplicateMessage.Payload.(*core.LobbyMessage).Duplicate {
		t.Fatalf("duplicate room message should return idempotent duplicate: %+v", duplicateMessage)
	}

	joined := handler.HandleWSSMessage(WSSMessage{
		Name:      "rooms.join",
		SessionID: guest.SessionToken,
		UserID:    guest.UserID,
		Payload: envelopePayload(3, "nonce-sql-room-join", "rooms_join", map[string]any{
			"room_code":      room.RoomCode,
			"mode_id":        "pvp_duel",
			"active_deck_id": "sql_guest_deck",
			"deck_snapshot":  deckPayload("sql_guest_deck"),
		}),
	})
	if !joined.OK {
		t.Fatalf("room join failed: %+v", joined)
	}
	match := joined.Payload.(*core.QueueResponse)
	if match.MatchID == "" || match.BattleTicket == nil {
		t.Fatalf("join should create match allocation and guest ticket: %+v", match)
	}

	polledTicket := handler.HandleWSSMessage(WSSMessage{
		Name:      "matchmaking.ticket",
		SessionID: guest.SessionToken,
		UserID:    guest.UserID,
		Payload: envelopePayload(4, "nonce-sql-guest-ticket-poll", "matchmaking_ticket", map[string]any{
			"ticket_id": match.TicketID,
		}),
	})
	if !polledTicket.OK {
		t.Fatalf("guest room ticket WSS poll failed: %+v", polledTicket)
	}
	polled := polledTicket.Payload.(*core.QueueResponse)
	if polled.TicketID != match.TicketID || polled.MatchID != match.MatchID || polled.BattleTicket == nil || polled.BattleAllocation == nil {
		t.Fatalf("guest room ticket WSS poll should preserve match allocation/ticket: %+v", polled)
	}

	roomEvent := handler.HandleWSSMessage(WSSMessage{
		Name:      "business.event",
		SessionID: host.SessionToken,
		UserID:    host.UserID,
		Payload: envelopePayload(4, "nonce-sql-host-room-event", "business_event", map[string]any{
			"kind":      "room",
			"room_code": room.RoomCode,
		}),
	})
	if !roomEvent.OK {
		t.Fatalf("host room business event failed: %+v", roomEvent)
	}
	if event := roomEvent.Payload.(*core.BusinessEvent); event.Room == nil || event.Room.RoomCode != room.RoomCode || event.ClientResultSubmitAllowed || event.HighFrequencyBattleTickAllowed {
		t.Fatalf("host room business event should expose audited low-frequency room state: %+v", event)
	}

	hostHeartbeat := handler.HandleRPC(RPCRequest{
		ID:        "presence.heartbeat",
		SessionID: host.SessionToken,
		UserID:    host.UserID,
		Payload: envelopePayload(5, "nonce-sql-host-heartbeat", "presence_heartbeat", map[string]any{
			"ticket_id":         room.TicketID,
			"match_id":          match.MatchID,
			"client_tick":       4,
			"last_event_cursor": 0,
		}),
	})
	if !hostHeartbeat.OK || hostHeartbeat.Payload.(*core.PresenceHeartbeatResponse).MatchID != match.MatchID {
		t.Fatalf("host heartbeat failed: %+v", hostHeartbeat)
	}
	guestHeartbeat := handler.HandleWSSMessage(WSSMessage{
		Name:      "presence.heartbeat",
		SessionID: guest.SessionToken,
		UserID:    guest.UserID,
		Payload: envelopePayload(5, "nonce-sql-guest-heartbeat", "presence_heartbeat", map[string]any{
			"ticket_id":         match.TicketID,
			"match_id":          match.MatchID,
			"client_tick":       5,
			"last_event_cursor": 0,
		}),
	})
	if !guestHeartbeat.OK || guestHeartbeat.Payload.(*core.PresenceHeartbeatResponse).MatchID != match.MatchID {
		t.Fatalf("guest heartbeat failed: %+v", guestHeartbeat)
	}

	hostReady := handler.HandleRPC(RPCRequest{
		ID:        "match.ready",
		SessionID: host.SessionToken,
		UserID:    host.UserID,
		Payload: envelopePayload(6, "nonce-sql-host-ready", "match_ready", map[string]any{
			"match_id": match.MatchID,
		}),
	})
	if !hostReady.OK || hostReady.Payload.(*core.ReadyResponse).ReadyStatus != "loading" {
		t.Fatalf("host ready failed: %+v", hostReady)
	}
	guestReady := handler.HandleWSSMessage(WSSMessage{
		Name:      "match.ready",
		SessionID: guest.SessionToken,
		UserID:    guest.UserID,
		Payload: envelopePayload(6, "nonce-sql-guest-ready", "match_ready", map[string]any{
			"match_id": match.MatchID,
		}),
	})
	if !guestReady.OK || guestReady.Payload.(*core.ReadyResponse).ReadyStatus != "running" || guestReady.Payload.(*core.ReadyResponse).MatchStart == nil {
		t.Fatalf("guest ready failed: %+v", guestReady)
	}
	duplicateReady := handler.HandleRPC(RPCRequest{
		ID:        "match.ready",
		SessionID: host.SessionToken,
		UserID:    host.UserID,
		Payload: envelopePayload(7, "nonce-sql-host-ready-duplicate", "match_ready", map[string]any{
			"match_id": match.MatchID,
		}),
	})
	if !duplicateReady.OK || duplicateReady.Payload.(*core.ReadyResponse).ReadyCount != 2 {
		t.Fatalf("duplicate ready should remain idempotent: %+v", duplicateReady)
	}

	disconnected := handler.HandleWSSMessage(WSSMessage{
		Name:      "match.disconnect",
		SessionID: host.SessionToken,
		UserID:    host.UserID,
		Payload: envelopePayload(8, "nonce-sql-host-disconnect", "match_disconnect", map[string]any{
			"match_id": match.MatchID,
		}),
	})
	if !disconnected.OK || disconnected.Payload.(*core.ReconnectResponse).ReconnectStatus != "disconnected" {
		t.Fatalf("host disconnect failed: %+v", disconnected)
	}
	reconnected := handler.HandleRPC(RPCRequest{
		ID:        "match.reconnect",
		SessionID: host.SessionToken,
		UserID:    host.UserID,
		Payload: envelopePayload(9, "nonce-sql-host-reconnect", "match_reconnect", map[string]any{
			"match_id": match.MatchID,
		}),
	})
	if !reconnected.OK || reconnected.Payload.(*core.ReconnectResponse).ReconnectStatus != "restored" || reconnected.Payload.(*core.ReconnectResponse).BattleTicket == nil {
		t.Fatalf("host reconnect failed: %+v", reconnected)
	}

	ticket := handler.HandleRPC(RPCRequest{
		ID:        "battle.ticket",
		SessionID: host.SessionToken,
		UserID:    host.UserID,
		Payload: envelopePayload(10, "nonce-sql-host-ticket", "battle_ticket", map[string]any{
			"match_id": match.MatchID,
		}),
	})
	if !ticket.OK {
		t.Fatalf("host battle ticket failed: %+v", ticket)
	}
	allocation := ticket.Payload.(*core.SignedBattleTicket)
	publicConsume := handler.HandleRPC(RPCRequest{
		ID:        "battle.ticket.consume",
		SessionID: host.SessionToken,
		UserID:    host.UserID,
		Payload: envelopePayload(11, "nonce-public-ticket-consume", "battle_ticket_consume", map[string]any{
			"ticket_id":        allocation.Ticket.TicketID,
			"match_id":         match.MatchID,
			"battle_server_id": allocation.Ticket.BattleServerID,
			"ticket_nonce_hex": allocation.Ticket.TicketNonceHex,
		}),
	})
	if publicConsume.OK || publicConsume.Status != 403 || publicConsume.ErrorCode != CodeServiceOriginRequired {
		t.Fatalf("client-origin ticket consume should fail closed: %+v", publicConsume)
	}
	consumedTicket := handler.HandleRPC(RPCRequest{
		ID:      "battle.ticket.consume",
		Service: true,
		Payload: map[string]any{
			"version":          allocation.Ticket.Version,
			"ticket_id":        allocation.Ticket.TicketID,
			"match_id":         match.MatchID,
			"user_id":          allocation.Ticket.UserID,
			"player_id":        allocation.Ticket.PlayerID,
			"battle_server_id": allocation.Ticket.BattleServerID,
			"ticket_nonce_hex": allocation.Ticket.TicketNonceHex,
		},
	})
	if !consumedTicket.OK {
		t.Fatalf("service-origin battle ticket consume failed: %+v", consumedTicket)
	}
	if receipt := consumedTicket.Payload.(*core.BattleTicketConsumeResponse); !receipt.Consumed || receipt.Duplicate || receipt.TicketID != allocation.Ticket.TicketID || !receipt.ServerAuthoritative {
		t.Fatalf("ticket consume receipt should be accepted and authoritative: %+v", receipt)
	}
	if receipt := consumedTicket.Payload.(*core.BattleTicketConsumeResponse); receipt.IssuedAtMS != allocation.Ticket.IssuedAtMS || receipt.ExpiresAtMS != allocation.Ticket.ExpiresAtMS || receipt.ConsumedAtMS == 0 {
		t.Fatalf("ticket consume receipt should expose ticket lifecycle timestamps: ticket=%+v receipt=%+v", allocation.Ticket, receipt)
	}
	duplicateConsumedTicket := handler.HandleRPC(RPCRequest{
		ID:      "battle.ticket.consume",
		Service: true,
		Payload: map[string]any{
			"version":          allocation.Ticket.Version,
			"ticket_id":        allocation.Ticket.TicketID,
			"match_id":         match.MatchID,
			"battle_server_id": allocation.Ticket.BattleServerID,
			"ticket_nonce_hex": allocation.Ticket.TicketNonceHex,
		},
	})
	if !duplicateConsumedTicket.OK {
		t.Fatalf("duplicate service-origin battle ticket consume failed: %+v", duplicateConsumedTicket)
	}
	if receipt := duplicateConsumedTicket.Payload.(*core.BattleTicketConsumeResponse); !receipt.Consumed || !receipt.Duplicate || !receipt.ServerAuthoritative {
		t.Fatalf("duplicate ticket consume receipt should be idempotent and authoritative: %+v", receipt)
	}
	if first, duplicate := consumedTicket.Payload.(*core.BattleTicketConsumeResponse), duplicateConsumedTicket.Payload.(*core.BattleTicketConsumeResponse); duplicate.ConsumedAtMS != first.ConsumedAtMS || duplicate.ExpiresAtMS != first.ExpiresAtMS {
		t.Fatalf("duplicate ticket consume should preserve original lifecycle timestamps: first=%+v duplicate=%+v", first, duplicate)
	}
	freshTicket := handler.HandleRPC(RPCRequest{
		ID:        "battle.ticket",
		SessionID: host.SessionToken,
		UserID:    host.UserID,
		Payload: envelopePayload(15, "nonce-sql-host-ticket-after-consume", "battle_ticket", map[string]any{
			"match_id": match.MatchID,
		}),
	})
	if !freshTicket.OK {
		t.Fatalf("fresh host battle ticket failed: %+v", freshTicket)
	}
	freshAllocation := freshTicket.Payload.(*core.SignedBattleTicket)
	staleConsumeVersion := freshAllocation.Ticket.Version
	staleConsumeVersion.RulesetVersion = "ruleset-stale"
	staleConsumedTicket := handler.HandleRPC(RPCRequest{
		ID:      "battle.ticket.consume",
		Service: true,
		Payload: map[string]any{
			"version":          staleConsumeVersion,
			"ticket_id":        freshAllocation.Ticket.TicketID,
			"match_id":         match.MatchID,
			"battle_server_id": freshAllocation.Ticket.BattleServerID,
			"ticket_nonce_hex": freshAllocation.Ticket.TicketNonceHex,
		},
	})
	if staleConsumedTicket.OK || staleConsumedTicket.Status != 400 || staleConsumedTicket.ErrorCode != "invalid_request" || !strings.Contains(staleConsumedTicket.Message, "ruleset version mismatch") {
		t.Fatalf("service-origin ticket consume must reject stale version stamp: %+v", staleConsumedTicket)
	}
	playerIDs := []string{}
	for _, player := range match.BattleAllocation.Players {
		playerIDs = append(playerIDs, player.PlayerID)
	}
	badProjectionSubmit := handler.HandleRPC(RPCRequest{
		ID:      "battle.result.submit",
		Service: true,
		Payload: map[string]any{"signed_result": map[string]any{
			"ok": true,
			"result": map[string]any{
				"version": map[string]any{
					"protocol_version":     core.ProtocolVersion,
					"business_api_version": core.BusinessAPIVersion,
					"battle_api_version":   core.BattleAPIVersion,
					"ruleset_version":      core.RulesetVersion,
				},
				"match_id":         match.MatchID,
				"mode_id":          match.ModeID,
				"result_hash":      "sha256:abcd1234",
				"replay_id":        "nakama-replay-forbidden-callback",
				"player_ids":       playerIDs,
				"mode_result_json": `{"verified":true,"players":[{"boss_hp":0}]}`,
				"settled_at_ms":    time.Now().UnixMilli(),
			},
			"signature_alg":        "ED25519",
			"key_id":               match.BattleAllocation.BattleServerID,
			"signature_hex":        strings.Repeat("c", 128),
			"public_key_hex":       strings.Repeat("d", 64),
			"server_authoritative": true,
		}},
	})
	if badProjectionSubmit.OK || badProjectionSubmit.Status != 403 || badProjectionSubmit.ErrorCode != "forbidden_field" {
		t.Fatalf("service-origin result projection with authority fields should be rejected: %+v", badProjectionSubmit)
	}

	resultSubmit := handler.HandleRPC(RPCRequest{
		ID:      "battle.result.submit",
		Service: true,
		Payload: map[string]any{"signed_result": map[string]any{
			"ok": true,
			"result": map[string]any{
				"version": map[string]any{
					"protocol_version":     core.ProtocolVersion,
					"business_api_version": core.BusinessAPIVersion,
					"battle_api_version":   core.BattleAPIVersion,
					"ruleset_version":      core.RulesetVersion,
				},
				"match_id":               match.MatchID,
				"mode_id":                match.ModeID,
				"result_hash":            "sha256:abcd1234",
				"replay_id":              "nakama-sql-replay",
				"player_ids":             playerIDs,
				"reward_projection_json": `{"source":"battle_server"}`,
				"mode_result_json":       `{"verified":true}`,
				"settled_at_ms":          time.Now().UnixMilli(),
			},
			"signature_alg":        "ED25519",
			"key_id":               allocation.Ticket.BattleServerID,
			"signature_hex":        strings.Repeat("a", 128),
			"public_key_hex":       strings.Repeat("b", 64),
			"server_authoritative": true,
		}},
	})
	if !resultSubmit.OK {
		t.Fatalf("service-origin battle result submit failed: %+v", resultSubmit)
	}
	if receipt := resultSubmit.Payload.(*core.BattleResultSubmitResponse); !receipt.Accepted || receipt.Duplicate || !receipt.ServerAuthoritative {
		t.Fatalf("result submit receipt should be accepted and authoritative: %+v", receipt)
	}
	duplicateResultSubmit := handler.HandleRPC(RPCRequest{
		ID:      "battle.result.submit",
		Service: true,
		Payload: map[string]any{"signed_result": map[string]any{
			"ok": true,
			"result": map[string]any{
				"version": map[string]any{
					"protocol_version":     core.ProtocolVersion,
					"business_api_version": core.BusinessAPIVersion,
					"battle_api_version":   core.BattleAPIVersion,
					"ruleset_version":      core.RulesetVersion,
				},
				"match_id":               match.MatchID,
				"mode_id":                match.ModeID,
				"result_hash":            "sha256:abcd1234",
				"replay_id":              "nakama-sql-replay",
				"player_ids":             playerIDs,
				"reward_projection_json": `{"source":"battle_server"}`,
				"mode_result_json":       `{"verified":true}`,
				"settled_at_ms":          time.Now().UnixMilli(),
			},
			"signature_alg":        "ED25519",
			"key_id":               allocation.Ticket.BattleServerID,
			"signature_hex":        strings.Repeat("a", 128),
			"public_key_hex":       strings.Repeat("b", 64),
			"server_authoritative": true,
		}},
	})
	if !duplicateResultSubmit.OK {
		t.Fatalf("duplicate service-origin battle result submit failed: %+v", duplicateResultSubmit)
	}
	if receipt := duplicateResultSubmit.Payload.(*core.BattleResultSubmitResponse); !receipt.Accepted || !receipt.Duplicate || !receipt.ServerAuthoritative {
		t.Fatalf("duplicate result submit receipt should be idempotent and authoritative: %+v", receipt)
	}
	settlementEvent := handler.HandleWSSMessage(WSSMessage{
		Name:      "business.event.settlement",
		SessionID: host.SessionToken,
		UserID:    host.UserID,
		Payload: envelopePayload(16, "nonce-sql-host-settlement-event", "business_event_settlement", map[string]any{
			"match_id": match.MatchID,
		}),
	})
	if !settlementEvent.OK || settlementEvent.Status != 200 {
		t.Fatalf("settlement business event failed: %+v", settlementEvent)
	}
	settlementPayload := settlementEvent.Payload.(*core.BusinessEvent)
	if settlementPayload.Topic != "nakama_wss.business.settlement" || settlementPayload.MatchID != match.MatchID || settlementPayload.Settlement == nil || settlementPayload.Settlement.MatchID != match.MatchID || settlementPayload.Settlement.ModeResult["battle_result_hash"] != "sha256:abcd1234" {
		t.Fatalf("settlement business event should project verified server settlement: %+v", settlementPayload)
	}
	if settlementPayload.HighFrequencyBattleTickAllowed || settlementPayload.ClientResultSubmitAllowed || stringSliceContains(settlementPayload.AllowedClientOperations, "battle.result.submit") {
		t.Fatalf("settlement business event must not authorize battle tick or client result submit: %+v", settlementPayload)
	}
	if !settlementPayload.BusinessEnvelopeRequired || !stringSliceContains(settlementPayload.ForbiddenFields, "final_result") {
		t.Fatalf("settlement business event should retain business security contract: %+v", settlementPayload)
	}
	if !stringSliceContains(settlementPayload.BusinessNotifications, "settlement") || stringSliceContains(settlementPayload.BusinessNotifications, "battle.result.submit") {
		t.Fatalf("settlement business event should retain low-frequency WSS notification contract: %+v", settlementPayload)
	}
	clientAuthoredSettlement := handler.HandleRPC(RPCRequest{
		ID:        "business.event.settlement",
		SessionID: host.SessionToken,
		UserID:    host.UserID,
		Payload: envelopePayload(17, "nonce-sql-host-settlement-client-authored", "business_event_settlement", map[string]any{
			"match_id":    match.MatchID,
			"result_hash": "client-authored",
		}),
	})
	if clientAuthoredSettlement.OK || clientAuthoredSettlement.Status != 403 || clientAuthoredSettlement.ErrorCode != "forbidden_field" {
		t.Fatalf("client-authored settlement fields must be rejected before dispatch: %+v", clientAuthoredSettlement)
	}
	battleStatus := handler.HandleRPC(RPCRequest{
		ID:        "battle.audit.status",
		SessionID: host.SessionToken,
		UserID:    host.UserID,
		Payload:   envelopePayload(18, "nonce-sql-battle-status", "battle_audit_status", map[string]any{}),
	})
	if !battleStatus.OK {
		t.Fatalf("battle audit status failed: %+v", battleStatus)
	}
	if status := battleStatus.Payload.(core.BattleLifecycleAuditStatus); !status.OK || !status.Configured || status.ServerLifecycleRecords != 4 || status.AllocationRecords != 1 || status.TicketRecords < 2 || status.TicketConsumedRecords != 1 || status.ResultRecords != 1 || status.ResultDuplicateRecords != 1 || status.ReplayRecords != 2 || status.LastSuccessOperation != "battle_result_duplicate" || !strings.HasPrefix(status.LastSuccessFingerprint, "sha256:") || status.LastSuccessAt.IsZero() {
		t.Fatalf("battle audit status should reflect SQL repository writes: %+v", status)
	}
	lobbyStatus := handler.HandleWSSMessage(WSSMessage{
		Name:      "lobby.audit.status",
		SessionID: guest.SessionToken,
		UserID:    guest.UserID,
		Payload:   envelopePayload(7, "nonce-sql-lobby-status", "lobby_audit_status", map[string]any{}),
	})
	if !lobbyStatus.OK {
		t.Fatalf("lobby audit status failed: %+v", lobbyStatus)
	}
	if status := lobbyStatus.Payload.(core.LobbyLifecycleAuditStatus); !status.OK || !status.Configured || status.RoomRecords != 3 || status.RoomReadRecords != 3 || status.ReadyRecords != 2 || status.ConnectionRecords != 4 || status.MessageRecords != 2 || status.LastSuccessOperation != "reconnected" || !strings.HasPrefix(status.LastSuccessFingerprint, "sha256:") || status.LastSuccessAt.IsZero() {
		t.Fatalf("lobby audit status should reflect SQL repository writes: %+v", status)
	}

	tableCounts := nakamaSQLTableCounts()
	if tableCounts["business_envelope_audits"] < 17 || tableCounts["lobby_room_audits"] != 12 || tableCounts["lobby_message_audits"] != 2 || tableCounts["match_allocation_audits"] != 5 || tableCounts["battle_ticket_audits"] < 3 || tableCounts["battle_result_audits"] != 2 || tableCounts["replay_audits"] != 2 {
		t.Fatalf("unexpected SQL audit inserts: counts=%+v calls=%+v", tableCounts, nakamaSQLCaptureCalls())
	}
	if !nakamaSQLHasBattleServerLifecycleAudits() {
		t.Fatalf("expected service-origin battle server lifecycle audit rows: calls=%+v", nakamaSQLCaptureCalls())
	}
	if !nakamaSQLHasDuplicateLobbyMessageAudit() {
		t.Fatalf("expected duplicate lobby message audit row: calls=%+v", nakamaSQLCaptureCalls())
	}
	if !nakamaSQLHasDuplicateBattleResultAudit() {
		t.Fatalf("expected duplicate battle result audit row: calls=%+v", nakamaSQLCaptureCalls())
	}
	if !nakamaSQLHasConsumedBattleTicketAudit() {
		t.Fatalf("expected consumed battle ticket audit row: calls=%+v", nakamaSQLCaptureCalls())
	}
}

func TestNakamaEnvelopeAuditStatusReportsSQLSinkFailures(t *testing.T) {
	driverName := registerNakamaSQLCaptureDriver(t, "business_envelope_audits")
	db, err := sql.Open(driverName, "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	handler, err := NewWithDatabase(db)
	if err != nil {
		t.Fatal(err)
	}
	login := handler.HandleRPC(RPCRequest{
		ID:      "auth.anonymous",
		Payload: map[string]any{"device_id": "device-sql-envelope-fail", "display_name": "SQL Envelope Fail"},
	})
	if !login.OK {
		t.Fatalf("login failed: %+v", login)
	}
	session := login.Payload.(*core.AuthSession)

	bootstrap := handler.HandleRPC(RPCRequest{
		ID:        "bootstrap",
		SessionID: session.SessionToken,
		UserID:    session.UserID,
		Payload:   envelopePayload(1, "nonce-sql-envelope-bootstrap", "bootstrap", map[string]any{}),
	})
	if !bootstrap.OK {
		t.Fatalf("business dispatch should continue while envelope audit failure is surfaced: %+v", bootstrap)
	}
	status := handler.HandleWSSMessage(WSSMessage{
		Name:      "business.envelope.audit.status",
		SessionID: session.SessionToken,
		UserID:    session.UserID,
		Payload:   envelopePayload(2, "nonce-sql-envelope-status", "business_envelope_audit_status", map[string]any{}),
	})
	if !status.OK || status.Status != 200 {
		t.Fatalf("envelope audit status failed: %+v", status)
	}
	snapshot := status.Payload.(security.BusinessEnvelopeGuardSnapshot)
	if snapshot.Accepted != 2 || snapshot.AuditErrors != 2 || snapshot.SessionCount != 1 {
		t.Fatalf("envelope audit status should expose durable sink failures: %+v", snapshot)
	}
	if tableCounts := nakamaSQLTableCounts(); tableCounts["business_envelope_audits"] != 2 {
		t.Fatalf("expected two attempted envelope audit inserts, counts=%+v calls=%+v", tableCounts, nakamaSQLCaptureCalls())
	}
}

func TestNakamaHandlerDatabaseWiringReportsLifecycleAuditFailures(t *testing.T) {
	driverName := registerNakamaSQLCaptureDriver(t, "lobby_room_audits", "match_allocation_audits")
	db, err := sql.Open(driverName, "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	handler, err := NewWithDatabase(db)
	if err != nil {
		t.Fatal(err)
	}
	hostLogin := handler.HandleRPC(RPCRequest{
		ID:      "auth.anonymous",
		Payload: map[string]any{"device_id": "device-sql-fail-host", "display_name": "SQL Fail Host"},
	})
	if !hostLogin.OK {
		t.Fatalf("host login failed: %+v", hostLogin)
	}
	host := hostLogin.Payload.(*core.AuthSession)
	guestLogin := handler.HandleRPC(RPCRequest{
		ID:      "auth.anonymous",
		Payload: map[string]any{"device_id": "device-sql-fail-guest", "display_name": "SQL Fail Guest"},
	})
	if !guestLogin.OK {
		t.Fatalf("guest login failed: %+v", guestLogin)
	}
	guest := guestLogin.Payload.(*core.AuthSession)

	created := handler.HandleRPC(RPCRequest{
		ID:        "rooms.create",
		SessionID: host.SessionToken,
		UserID:    host.UserID,
		Payload: envelopePayload(1, "nonce-sql-fail-room-create", "rooms_create", map[string]any{
			"mode_id":        "pvp_duel",
			"active_deck_id": "sql_fail_host_deck",
			"deck_snapshot":  deckPayload("sql_fail_host_deck"),
		}),
	})
	if !created.OK {
		t.Fatalf("room create should continue while lobby audit failure is surfaced: %+v", created)
	}
	room := created.Payload.(*core.QueueResponse)

	joined := handler.HandleWSSMessage(WSSMessage{
		Name:      "rooms.join",
		SessionID: guest.SessionToken,
		UserID:    guest.UserID,
		Payload: envelopePayload(1, "nonce-sql-fail-room-join", "rooms_join", map[string]any{
			"room_code":      room.RoomCode,
			"mode_id":        "pvp_duel",
			"active_deck_id": "sql_fail_guest_deck",
			"deck_snapshot":  deckPayload("sql_fail_guest_deck"),
		}),
	})
	if !joined.OK {
		t.Fatalf("room join should continue while allocation audit failure is surfaced: %+v", joined)
	}

	lobbyStatus := handler.HandleRPC(RPCRequest{
		ID:        "lobby.audit.status",
		SessionID: host.SessionToken,
		UserID:    host.UserID,
		Payload:   envelopePayload(2, "nonce-sql-fail-lobby-status", "lobby_audit_status", map[string]any{}),
	})
	if !lobbyStatus.OK {
		t.Fatalf("lobby audit status failed: %+v", lobbyStatus)
	}
	lobby := lobbyStatus.Payload.(core.LobbyLifecycleAuditStatus)
	if lobby.OK || !lobby.Configured || lobby.RejectedRecords == 0 || lobby.LastErrorOperation != "matched" || !strings.Contains(lobby.LastError, "forced SQL failure for lobby_room_audits") {
		t.Fatalf("lobby audit status should expose SQL repository failure: %+v", lobby)
	}

	battleStatus := handler.HandleWSSMessage(WSSMessage{
		Name:      "battle.audit.status",
		SessionID: guest.SessionToken,
		UserID:    guest.UserID,
		Payload:   envelopePayload(2, "nonce-sql-fail-battle-status", "battle_audit_status", map[string]any{}),
	})
	if !battleStatus.OK {
		t.Fatalf("battle audit status failed: %+v", battleStatus)
	}
	battle := battleStatus.Payload.(core.BattleLifecycleAuditStatus)
	if battle.OK || !battle.Configured || battle.RejectedRecords == 0 || battle.LastErrorOperation != "match_allocation" || !strings.Contains(battle.LastError, "forced SQL failure for match_allocation_audits") {
		t.Fatalf("battle audit status should expose SQL repository failure: %+v", battle)
	}
}

func envelopePayload(seq int64, nonce string, op string, body map[string]any) map[string]any {
	return envelopePayloadAt(seq, time.Now(), nonce, op, body)
}

func envelopePayloadAt(seq int64, timestamp time.Time, nonce string, op string, body map[string]any) map[string]any {
	return map[string]any{
		security.BusinessEnvelopePayloadKey: map[string]any{
			"version":         security.BusinessEnvelopeVersion,
			"seq":             seq,
			"timestamp_ms":    timestamp.UnixMilli(),
			"nonce":           nonce,
			"op_code":         op,
			"key_id":          "client-dev-key",
			"auth_tag":        fmt.Sprintf("%064x", seq),
			"ciphertext_mode": "plain-scaffold",
			"body_hash":       fmt.Sprintf("body-%d", seq),
		},
		"body": body,
	}
}

func deckPayload(deckID string) map[string]any {
	return map[string]any{
		"deck_id":         deckID,
		"name":            deckID,
		"ruleset_version": core.RulesetVersion,
		"card_ids": []any{
			"focus_lens", "hitbox_charm", "density_surge", "tempo_break", "bomb_amplifier",
			"guard_seal", "graze_engine", "draw_sigil", "aim_baffle", "purge_charm",
			"focus_lens", "hitbox_charm", "density_surge", "tempo_break", "bomb_amplifier",
			"guard_seal", "graze_engine", "draw_sigil", "aim_baffle", "purge_charm",
		},
	}
}

type nakamaSQLCaptureCall struct {
	query string
	args  []any
}

var nakamaSQLCaptureState = struct {
	sync.Mutex
	nextID int
	calls  []nakamaSQLCaptureCall
	failOn map[string]bool
}{}

func registerNakamaSQLCaptureDriver(t *testing.T, failOnTables ...string) string {
	t.Helper()
	nakamaSQLCaptureState.Lock()
	defer nakamaSQLCaptureState.Unlock()
	nakamaSQLCaptureState.nextID++
	nakamaSQLCaptureState.calls = nil
	nakamaSQLCaptureState.failOn = map[string]bool{}
	for _, table := range failOnTables {
		nakamaSQLCaptureState.failOn[table] = true
	}
	name := fmt.Sprintf("nakama_sql_capture_driver_%d", nakamaSQLCaptureState.nextID)
	sql.Register(name, nakamaSQLCaptureDriver{})
	return name
}

func nakamaSQLCaptureCalls() []nakamaSQLCaptureCall {
	nakamaSQLCaptureState.Lock()
	defer nakamaSQLCaptureState.Unlock()
	calls := make([]nakamaSQLCaptureCall, len(nakamaSQLCaptureState.calls))
	copy(calls, nakamaSQLCaptureState.calls)
	return calls
}

func nakamaSQLTableCounts() map[string]int {
	counts := map[string]int{}
	for _, call := range nakamaSQLCaptureCalls() {
		for _, table := range []string{
			"business_envelope_audits",
			"lobby_room_audits",
			"lobby_message_audits",
			"match_allocation_audits",
			"battle_ticket_audits",
			"battle_result_audits",
			"replay_audits",
		} {
			if strings.Contains(call.query, "INSERT INTO "+table) {
				counts[table]++
			}
		}
	}
	return counts
}

func nakamaSQLHasDuplicateLobbyMessageAudit() bool {
	for _, call := range nakamaSQLCaptureCalls() {
		if !strings.Contains(call.query, "INSERT INTO lobby_message_audits") || len(call.args) < 10 {
			continue
		}
		if call.args[0] == "sql-host-message-1" && call.args[5] == true && call.args[9] == true {
			return true
		}
	}
	return false
}

func nakamaSQLHasDuplicateBattleResultAudit() bool {
	for _, call := range nakamaSQLCaptureCalls() {
		if !strings.Contains(call.query, "INSERT INTO battle_result_audits") || len(call.args) < 13 {
			continue
		}
		if strings.HasPrefix(fmt.Sprint(call.args[0]), "match_") && call.args[3] == "sha256:abcd1234" && call.args[8] == "duplicate" && call.args[9] == "" && call.args[12] == true {
			return true
		}
	}
	return false
}

func nakamaSQLHasConsumedBattleTicketAudit() bool {
	for _, call := range nakamaSQLCaptureCalls() {
		if !strings.Contains(call.query, "INSERT INTO battle_ticket_audits") || len(call.args) < 18 {
			continue
		}
		if strings.HasPrefix(fmt.Sprint(call.args[0]), "battle_ticket_") && call.args[15] == "consumed" && call.args[16] == true && call.args[17] != nil {
			return true
		}
	}
	return false
}

func nakamaSQLHasBattleServerLifecycleAudits() bool {
	foundRegister := false
	foundHeartbeat := false
	foundOffline := false
	for _, call := range nakamaSQLCaptureCalls() {
		if !strings.Contains(call.query, "INSERT INTO match_allocation_audits") || len(call.args) < 14 {
			continue
		}
		if call.args[0] != "battle-server:nakama-sql-battle" || call.args[2] != "nakama-sql-battle" || call.args[12] != true {
			continue
		}
		switch call.args[11] {
		case "server_registered":
			foundRegister = true
		case "server_heartbeat":
			foundHeartbeat = true
		case "server_offline":
			foundOffline = true
		}
	}
	return foundRegister && foundHeartbeat && foundOffline
}

type nakamaSQLCaptureDriver struct{}

func (nakamaSQLCaptureDriver) Open(name string) (driver.Conn, error) {
	return nakamaSQLCaptureConn{}, nil
}

type nakamaSQLCaptureConn struct{}

func (nakamaSQLCaptureConn) Prepare(query string) (driver.Stmt, error) {
	return nakamaSQLCaptureStmt{query: query}, nil
}

func (nakamaSQLCaptureConn) Close() error {
	return nil
}

func (nakamaSQLCaptureConn) Begin() (driver.Tx, error) {
	return nakamaSQLCaptureTx{}, nil
}

func (nakamaSQLCaptureConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	values := make([]any, len(args))
	for i, arg := range args {
		values[i] = arg.Value
	}
	nakamaSQLCaptureState.Lock()
	nakamaSQLCaptureState.calls = append(nakamaSQLCaptureState.calls, nakamaSQLCaptureCall{query: query, args: values})
	err := nakamaSQLForcedErrorLocked(query)
	nakamaSQLCaptureState.Unlock()
	if err != nil {
		return nil, err
	}
	return driver.RowsAffected(1), nil
}

func (nakamaSQLCaptureConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	return nakamaSQLCaptureRows{}, nil
}

type nakamaSQLCaptureStmt struct {
	query string
}

func (stmt nakamaSQLCaptureStmt) Close() error {
	return nil
}

func (stmt nakamaSQLCaptureStmt) NumInput() int {
	return -1
}

func (stmt nakamaSQLCaptureStmt) Exec(args []driver.Value) (driver.Result, error) {
	values := make([]any, len(args))
	for i, arg := range args {
		values[i] = arg
	}
	nakamaSQLCaptureState.Lock()
	nakamaSQLCaptureState.calls = append(nakamaSQLCaptureState.calls, nakamaSQLCaptureCall{query: stmt.query, args: values})
	err := nakamaSQLForcedErrorLocked(stmt.query)
	nakamaSQLCaptureState.Unlock()
	if err != nil {
		return nil, err
	}
	return driver.RowsAffected(1), nil
}

func nakamaSQLForcedErrorLocked(query string) error {
	for table := range nakamaSQLCaptureState.failOn {
		if strings.Contains(query, "INSERT INTO "+table) {
			return fmt.Errorf("forced SQL failure for %s", table)
		}
	}
	return nil
}

func stringSliceContains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func (stmt nakamaSQLCaptureStmt) Query(args []driver.Value) (driver.Rows, error) {
	return nakamaSQLCaptureRows{}, nil
}

type nakamaSQLCaptureTx struct{}

func (nakamaSQLCaptureTx) Commit() error {
	return nil
}

func (nakamaSQLCaptureTx) Rollback() error {
	return nil
}

type nakamaSQLCaptureRows struct{}

func (nakamaSQLCaptureRows) Columns() []string {
	return nil
}

func (nakamaSQLCaptureRows) Close() error {
	return nil
}

func (nakamaSQLCaptureRows) Next(dest []driver.Value) error {
	return io.EOF
}
