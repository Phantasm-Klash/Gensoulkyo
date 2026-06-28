package nakamaapi

import (
	"fmt"
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

func envelopePayload(seq int64, nonce string, op string, body map[string]any) map[string]any {
	return map[string]any{
		security.BusinessEnvelopePayloadKey: map[string]any{
			"version":         security.BusinessEnvelopeVersion,
			"seq":             seq,
			"timestamp_ms":    time.Now().UnixMilli(),
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
