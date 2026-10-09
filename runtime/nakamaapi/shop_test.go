package nakamaapi

import (
	"testing"

	"gensoulkyo/runtime/core"
)

func TestNakamaShopRPCDispatch(t *testing.T) {
	handler := New(core.NewService(core.Config{}))
	login := handler.HandleRPC(RPCRequest{
		ID:      "auth.anonymous",
		Payload: map[string]any{"device_id": "shop-rpc", "display_name": "Shop RPC"},
	})
	if !login.OK {
		t.Fatalf("shop rpc login failed: %+v", login)
	}
	session := login.Payload.(*core.AuthSession)

	catalog := handler.HandleRPC(RPCRequest{
		ID:        "shop.catalog",
		SessionID: session.SessionToken,
		UserID:    session.UserID,
		Payload:   envelopePayload(1, "shop-rpc-catalog", "shop_catalog", map[string]any{}),
	})
	if !catalog.OK || catalog.Status != 200 {
		t.Fatalf("shop catalog RPC failed: %+v", catalog)
	}
	if payload, ok := catalog.Payload.(*core.ShopCatalogResponse); !ok || len(payload.Products) != 6 {
		t.Fatalf("shop catalog RPC payload invalid: %+v", catalog.Payload)
	}

	legacyCatalog := handler.HandleRPC(RPCRequest{
		ID:        "shop.get",
		SessionID: session.SessionToken,
		UserID:    session.UserID,
		Payload:   envelopePayload(2, "shop-rpc-get", "shop.get", map[string]any{}),
	})
	if !legacyCatalog.OK || legacyCatalog.Status != 200 {
		t.Fatalf("legacy shop.get RPC failed: %+v", legacyCatalog)
	}
	if payload, ok := legacyCatalog.Payload.(*core.ShopCatalogResponse); !ok || len(payload.Products) != 6 {
		t.Fatalf("legacy shop.get RPC payload invalid: %+v", legacyCatalog.Payload)
	}

	purchase := handler.HandleRPC(RPCRequest{
		ID:        "shop.purchase",
		SessionID: session.SessionToken,
		UserID:    session.UserID,
		Payload: envelopePayload(3, "shop-rpc-purchase", "shop_purchase", map[string]any{
			"product_id":  "card.focus_lens.single",
			"quantity":    1,
			"nonce":       "rpc-shop-nonce",
			"cost_amount": 1,
		}),
	})
	if purchase.OK || purchase.Status != 403 || purchase.ErrorCode != "forbidden_field" {
		t.Fatalf("shop purchase should reject client-authored price: %+v", purchase)
	}

	purchase = handler.HandleRPC(RPCRequest{
		ID:        "shop.purchase",
		SessionID: session.SessionToken,
		UserID:    session.UserID,
		Payload: envelopePayload(4, "shop-rpc-purchase-valid", "shop_purchase", map[string]any{
			"product_id": "card.focus_lens.single",
			"quantity":   1,
			"nonce":      "rpc-shop-nonce-valid",
		}),
	})
	if !purchase.OK || purchase.Status != 200 {
		t.Fatalf("shop purchase RPC failed: %+v", purchase)
	}
	if payload, ok := purchase.Payload.(*core.ShopPurchaseResponse); !ok || !payload.ServerAuthoritative || payload.Receipt.CostAmount != 200 {
		t.Fatalf("shop purchase RPC payload invalid: %+v", purchase.Payload)
	}
}
