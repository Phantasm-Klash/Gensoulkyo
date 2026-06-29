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

	joined := handler.HandleWSSMessage(WSSMessage{
		Name:      "rooms.join",
		SessionID: guest.SessionToken,
		UserID:    guest.UserID,
		Payload: envelopePayload(3, "nonce-room-join", "rooms_join", map[string]any{
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
		Payload: envelopePayload(4, "nonce-room-chat", "rooms_message", map[string]any{
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
		Payload: envelopePayload(5, "nonce-room-chat-duplicate", "rooms_message", map[string]any{
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
		Payload: envelopePayload(6, "nonce-room-chat-authority", "rooms_message", map[string]any{
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
		Payload: envelopePayload(7, "nonce-room-leave", "rooms_leave", map[string]any{
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
		Payload: envelopePayload(8, "nonce-room-forbidden", "rooms_create", map[string]any{
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
		Payload: envelopePayload(3, "nonce-external-guest-ready", "match_ready", map[string]any{
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
		Payload: envelopePayload(4, "nonce-external-guest-heartbeat", "presence_heartbeat", map[string]any{
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

	wssResultSubmit := handler.HandleWSSMessage(WSSMessage{
		Name:      "battle.result.submit",
		SessionID: hostSession,
		UserID:    hostUser,
		Payload: envelopePayload(8, "nonce-external-host-result-submit", "battle_result_submit", map[string]any{
			"match_id": found.MatchID,
		}),
	})
	if wssResultSubmit.OK || wssResultSubmit.Status != 404 {
		t.Fatalf("battle result submit must stay out of client WSS dispatch: %+v", wssResultSubmit)
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
	if snapshot := handler.EnvelopeSnapshot(); snapshot.Accepted != 0 || snapshot.Rejected != 0 {
		t.Fatalf("service-origin rejection should not consume envelope seq/nonce: %+v", snapshot)
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

	ticket := handler.HandleRPC(RPCRequest{
		ID:        "battle.ticket",
		SessionID: host.SessionToken,
		UserID:    host.UserID,
		Payload: envelopePayload(4, "nonce-sql-host-ticket", "battle_ticket", map[string]any{
			"match_id": match.MatchID,
		}),
	})
	if !ticket.OK {
		t.Fatalf("host battle ticket failed: %+v", ticket)
	}
	battleStatus := handler.HandleRPC(RPCRequest{
		ID:        "battle.audit.status",
		SessionID: host.SessionToken,
		UserID:    host.UserID,
		Payload:   envelopePayload(5, "nonce-sql-battle-status", "battle_audit_status", map[string]any{}),
	})
	if !battleStatus.OK {
		t.Fatalf("battle audit status failed: %+v", battleStatus)
	}
	if status := battleStatus.Payload.(core.BattleLifecycleAuditStatus); !status.OK || !status.Configured || status.AllocationRecords != 1 || status.TicketRecords < 2 {
		t.Fatalf("battle audit status should reflect SQL repository writes: %+v", status)
	}
	lobbyStatus := handler.HandleWSSMessage(WSSMessage{
		Name:      "lobby.audit.status",
		SessionID: guest.SessionToken,
		UserID:    guest.UserID,
		Payload:   envelopePayload(4, "nonce-sql-lobby-status", "lobby_audit_status", map[string]any{}),
	})
	if !lobbyStatus.OK {
		t.Fatalf("lobby audit status failed: %+v", lobbyStatus)
	}
	if status := lobbyStatus.Payload.(core.LobbyLifecycleAuditStatus); !status.OK || !status.Configured || status.RoomRecords != 2 || status.RoomReadRecords != 1 || status.MessageRecords != 2 {
		t.Fatalf("lobby audit status should reflect SQL repository writes: %+v", status)
	}

	tableCounts := nakamaSQLTableCounts()
	if tableCounts["business_envelope_audits"] < 8 || tableCounts["lobby_room_audits"] != 3 || tableCounts["lobby_message_audits"] != 2 || tableCounts["match_allocation_audits"] != 1 || tableCounts["battle_ticket_audits"] < 2 {
		t.Fatalf("unexpected SQL audit inserts: counts=%+v calls=%+v", tableCounts, nakamaSQLCaptureCalls())
	}
	if !nakamaSQLHasDuplicateLobbyMessageAudit() {
		t.Fatalf("expected duplicate lobby message audit row: calls=%+v", nakamaSQLCaptureCalls())
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
}{}

func registerNakamaSQLCaptureDriver(t *testing.T) string {
	t.Helper()
	nakamaSQLCaptureState.Lock()
	defer nakamaSQLCaptureState.Unlock()
	nakamaSQLCaptureState.nextID++
	nakamaSQLCaptureState.calls = nil
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
	nakamaSQLCaptureState.Unlock()
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
	nakamaSQLCaptureState.Unlock()
	return driver.RowsAffected(1), nil
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
