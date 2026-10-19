package nakamaapi

// abandon_rpc_test.go: cross-acceptance for the T10 rule-3 abandon intent on
// the Nakama RPC face. The HTTP route already honoured {"intent":"abandon"};
// both Nakama dispatch switches ignored it, so an explicit abandon degraded to
// a passive drop in nakama_rpc mode. This pins the fixed behaviour: the match
// settles immediately and a follow-up disconnect reports match_state_invalid.

import (
	"testing"

	"gensoulkyo/runtime/core"
)

func TestNakamaRPCAbandonIntentSettlesAtOnce(t *testing.T) {
	handler := New(core.NewService(core.Config{}))
	hostLogin := handler.HandleRPC(RPCRequest{
		ID:      "auth.anonymous",
		Payload: map[string]any{"device_id": "device-abandon-host", "display_name": "Abandon Host"},
	})
	if !hostLogin.OK {
		t.Fatalf("host login failed: %+v", hostLogin)
	}
	host := hostLogin.Payload.(*core.AuthSession)
	guestLogin := handler.HandleRPC(RPCRequest{
		ID:      "auth.anonymous",
		Payload: map[string]any{"device_id": "device-abandon-guest", "display_name": "Abandon Guest"},
	})
	if !guestLogin.OK {
		t.Fatalf("guest login failed: %+v", guestLogin)
	}
	guest := guestLogin.Payload.(*core.AuthSession)

	created := handler.HandleRPC(RPCRequest{
		ID:        "rooms.create",
		SessionID: host.SessionToken,
		UserID:    host.UserID,
		Payload: envelopePayload(1, "nonce-abandon-create", "rooms_create", map[string]any{
			"mode_id":        "mvp_boss_race",
			"active_deck_id": "abandon_host_deck",
			"deck_snapshot":  deckPayload("abandon_host_deck"),
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
		Payload: envelopePayload(1, "nonce-abandon-join", "rooms_join", map[string]any{
			"room_code":      roomCode,
			"mode_id":        "mvp_boss_race",
			"active_deck_id": "abandon_guest_deck",
			"deck_snapshot":  deckPayload("abandon_guest_deck"),
		}),
	})
	if !joined.OK {
		t.Fatalf("join room failed: %+v", joined)
	}
	matchID := joined.Payload.(*core.QueueResponse).MatchID
	if matchID == "" {
		t.Fatalf("expected a match after both players joined: %+v", joined.Payload)
	}

	// intent=abandon must settle immediately (rule 3), not start a reconnect.
	abandon := handler.HandleRPC(RPCRequest{
		ID:        "match.disconnect",
		SessionID: guest.SessionToken,
		UserID:    guest.UserID,
		Payload: envelopePayload(2, "nonce-abandon", "match_disconnect", map[string]any{
			"match_id": matchID,
			"intent":   "abandon",
		}),
	})
	if !abandon.OK || abandon.Status != 200 {
		t.Fatalf("abandon failed: %+v", abandon)
	}
	abandoned, ok := abandon.Payload.(*core.ReconnectResponse)
	if !ok {
		t.Fatalf("abandon payload type = %T", abandon.Payload)
	}
	if abandoned.ReconnectStatus != "abandoned" {
		t.Fatalf("abandon status = %q, want abandoned", abandoned.ReconnectStatus)
	}

	// The match is over: a second disconnect cannot reopen it.
	second := handler.HandleRPC(RPCRequest{
		ID:        "match.disconnect",
		SessionID: guest.SessionToken,
		UserID:    guest.UserID,
		Payload: envelopePayload(3, "nonce-abandon-again", "match_disconnect", map[string]any{
			"match_id": matchID,
		}),
	})
	if second.OK || second.ErrorCode != "match_state_invalid" {
		t.Fatalf("second disconnect = %+v, want match_state_invalid", second)
	}
}
