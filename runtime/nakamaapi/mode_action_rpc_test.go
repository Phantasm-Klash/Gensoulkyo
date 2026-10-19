package nakamaapi

// mode_action_rpc_test.go: cross-acceptance for `match.mode_action` on the
// Nakama RPC face. The client sends draft picks and in-match gifts through
// this op; it only existed as an HTTP route, so nakama_rpc mode 404'd at the
// Nakama layer (runtimeRPCIDs is the only registration source). This pins the
// registered behaviour: a gift mode action is accepted and lands on the match
// event stream as gift_sent.

import (
	"testing"

	"gensoulkyo/runtime/core"
)

func TestNakamaRPCModeActionDeliversGift(t *testing.T) {
	handler := New(core.NewService(core.Config{}))
	hostLogin := handler.HandleRPC(RPCRequest{
		ID:      "auth.anonymous",
		Payload: map[string]any{"device_id": "device-gift-host", "display_name": "Gift Host"},
	})
	if !hostLogin.OK {
		t.Fatalf("host login failed: %+v", hostLogin)
	}
	host := hostLogin.Payload.(*core.AuthSession)
	guestLogin := handler.HandleRPC(RPCRequest{
		ID:      "auth.anonymous",
		Payload: map[string]any{"device_id": "device-gift-guest", "display_name": "Gift Guest"},
	})
	if !guestLogin.OK {
		t.Fatalf("guest login failed: %+v", guestLogin)
	}
	guest := guestLogin.Payload.(*core.AuthSession)

	created := handler.HandleRPC(RPCRequest{
		ID:        "rooms.create",
		SessionID: host.SessionToken,
		UserID:    host.UserID,
		Payload: envelopePayload(1, "nonce-gift-create", "rooms_create", map[string]any{
			"mode_id":        "mvp_boss_race",
			"active_deck_id": "gift_host_deck",
			"deck_snapshot":  deckPayload("gift_host_deck"),
			"mode_params":    map[string]any{"stage_id": "lunar_maze"},
		}),
	})
	if !created.OK {
		t.Fatalf("create room failed: %+v", created)
	}
	roomCode := created.Payload.(*core.QueueResponse).RoomCode

	joined := handler.HandleRPC(RPCRequest{
		ID:        "rooms.join",
		SessionID: guest.SessionToken,
		UserID:    guest.UserID,
		Payload: envelopePayload(1, "nonce-gift-join", "rooms_join", map[string]any{
			"room_code":      roomCode,
			"mode_id":        "mvp_boss_race",
			"active_deck_id": "gift_guest_deck",
			"deck_snapshot":  deckPayload("gift_guest_deck"),
		}),
	})
	if !joined.OK {
		t.Fatalf("join room failed: %+v", joined)
	}
	matchID := joined.Payload.(*core.QueueResponse).MatchID
	if matchID == "" {
		t.Fatalf("expected a match after both players joined")
	}

	// Both seats ready: the match only reaches "running" after that, and a
	// gift is refused on a non-running match (match_state_invalid).
	for seq, session := range []*core.AuthSession{host, guest} {
		ready := handler.HandleRPC(RPCRequest{
			ID:        "match.ready",
			SessionID: session.SessionToken,
			UserID:    session.UserID,
			Payload: envelopePayload(int64(seq+2), "nonce-gift-ready-"+session.UserID, "match_ready", map[string]any{
				"match_id": matchID,
			}),
		})
		if !ready.OK {
			t.Fatalf("ready %s failed: %+v", session.UserID, ready)
		}
	}

	gift := handler.HandleRPC(RPCRequest{
		ID:        "match.mode_action",
		SessionID: guest.SessionToken,
		UserID:    guest.UserID,
		Payload: envelopePayload(4, "nonce-gift-send", "match_mode_action", map[string]any{
			"match_id":    matchID,
			"mode_id":     "mvp_boss_race",
			"action_type": "gift",
			"payload":     map[string]any{"gift_id": "heart"},
		}),
	})
	if !gift.OK || gift.Status != 200 {
		t.Fatalf("gift mode action failed: %+v", gift)
	}
	accepted, ok := gift.Payload.(*core.ModeActionResponse)
	if !ok {
		t.Fatalf("gift payload type = %T", gift.Payload)
	}
	if !accepted.Accepted || accepted.ActionType != "gift" {
		t.Fatalf("gift action = %+v, want accepted gift", accepted)
	}
	if accepted.Event.Type != "gift_sent" || accepted.Event.CardID != "heart" || accepted.Event.TargetUserID != host.UserID {
		t.Fatalf("gift event = %+v, want gift_sent heart -> host", accepted.Event)
	}

	// Unknown gift id is a coded rejection, not a silent accept.
	unknown := handler.HandleRPC(RPCRequest{
		ID:        "match.mode_action",
		SessionID: guest.SessionToken,
		UserID:    guest.UserID,
		Payload: envelopePayload(5, "nonce-gift-unknown", "match_mode_action", map[string]any{
			"match_id":    matchID,
			"mode_id":     "mvp_boss_race",
			"action_type": "gift",
			"payload":     map[string]any{"gift_id": "not_a_gift"},
		}),
	})
	// Action-level rejections come back as a rejected ModeActionResponse
	// (OK=true, Accepted=false, Status=rejected) rather than a transport error.
	if !unknown.OK {
		t.Fatalf("unknown gift transport = %+v, want a rejected payload", unknown)
	}
	rejected, ok := unknown.Payload.(*core.ModeActionResponse)
	if !ok || rejected.Accepted || rejected.Status != "rejected" || rejected.Reason == "" {
		t.Fatalf("unknown gift = %+v, want rejected with a reason", unknown.Payload)
	}
}
