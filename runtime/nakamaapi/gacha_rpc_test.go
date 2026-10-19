package nakamaapi

// gacha_rpc_test.go: the Nakama RPC face of the T10 gacha surface. Proves the
// four op ids are registered (runtimeRPCIDs) and dispatched by the lower-case
// switch cases, and that pulls flow through the business envelope guard.

import (
	"testing"

	"gensoulkyo/runtime/core"
)

func TestNakamaRPCDispatchesGachaSurface(t *testing.T) {
	handler := New(core.NewService(core.Config{}))
	login := handler.HandleRPC(RPCRequest{
		ID:      "auth.anonymous",
		Payload: map[string]any{"device_id": "device-gacha-rpc", "display_name": "Gacha RPC"},
	})
	if !login.OK {
		t.Fatalf("login failed: %+v", login)
	}
	session := login.Payload.(*core.AuthSession)

	// Lookup ops also require the envelope on the RPC face.
	missing := handler.HandleRPC(RPCRequest{
		ID:        "gacha.pools",
		SessionID: session.SessionToken,
		UserID:    session.UserID,
		Payload:   map[string]any{},
	})
	if missing.OK {
		t.Fatalf("gacha.pools without an envelope must be refused: %+v", missing)
	}

	pools := handler.HandleRPC(RPCRequest{
		ID:        "gacha.pools",
		SessionID: session.SessionToken,
		UserID:    session.UserID,
		Payload:   envelopePayload(1, "nonce-gacha-pools", "gacha_pools", map[string]any{}),
	})
	if !pools.OK || pools.Status != 200 {
		t.Fatalf("gacha.pools failed: %+v", pools)
	}
	poolsPayload, ok := pools.Payload.(*core.GachaPoolsResponse)
	if !ok || len(poolsPayload.Pools) != 2 {
		t.Fatalf("gacha.pools payload = %T %+v", pools.Payload, pools.Payload)
	}

	roster := handler.HandleRPC(RPCRequest{
		ID:        "gacha.roster",
		SessionID: session.SessionToken,
		UserID:    session.UserID,
		Payload:   envelopePayload(2, "nonce-gacha-roster", "gacha_roster", map[string]any{}),
	})
	if !roster.OK || roster.Status != 200 {
		t.Fatalf("gacha.roster failed: %+v", roster)
	}
	rosterPayload, ok := roster.Payload.(*core.CharacterRosterSnapshot)
	if !ok || len(rosterPayload.Shots) != 2 {
		t.Fatalf("gacha.roster payload = %T %+v", roster.Payload, roster.Payload)
	}

	pull := handler.HandleRPC(RPCRequest{
		ID:        "gacha.pull",
		SessionID: session.SessionToken,
		UserID:    session.UserID,
		Payload: envelopePayload(3, "nonce-gacha-pull", "gacha_pull", map[string]any{
			"pool_id": "gacha.characters",
			"count":   1,
		}),
	})
	if !pull.OK || pull.Status != 200 {
		t.Fatalf("gacha.pull failed: %+v", pull)
	}
	pullPayload, ok := pull.Payload.(*core.GachaPullResponse)
	if !ok || len(pullPayload.Results) != 1 || pullPayload.Faith != 1 {
		t.Fatalf("gacha.pull payload = %T %+v", pull.Payload, pull.Payload)
	}

	// Exchange: a fresh identity has no faith, so the op must fail closed
	// through the RPC face too (the success path is a core test).
	exchange := handler.HandleRPC(RPCRequest{
		ID:        "gacha.faith.exchange",
		SessionID: session.SessionToken,
		UserID:    session.UserID,
		Payload: envelopePayload(4, "nonce-gacha-exchange", "gacha_faith_exchange", map[string]any{
			"item_id": "MarisaB2",
		}),
	})
	if exchange.OK {
		t.Fatalf("faith exchange with 0 faith must fail: %+v", exchange)
	}
}
