package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"gensoulkyo/runtime/core"
	"gensoulkyo/runtime/security"
)

func TestHTTPShopCatalogAndPurchaseRoutes(t *testing.T) {
	service := core.NewService(core.Config{})
	server := httptest.NewServer(New(service))
	defer server.Close()

	alice := postJSON[core.AuthSession](t, server.URL+"/v1/auth/anonymous", "", map[string]any{
		"device_id":    "shop-http",
		"display_name": "Shop HTTP",
	})
	catalog := getJSON[core.ShopCatalogResponse](t, server.URL+"/v1/shop/catalog", alice.SessionToken)
	if len(catalog.Products) != 6 || catalog.Wallet["gold"] != 2000 {
		t.Fatalf("shop catalog route invalid: %+v", catalog)
	}
	purchase := postJSONWithHeaders[core.ShopPurchaseResponse](t, server.URL+"/v1/shop/purchase", alice.SessionToken, map[string]any{
		"product_id": "chest.standard.pull",
		"quantity":   1,
		"nonce":      "http-shop-nonce",
	}, businessEnvelopeHeaders(1, time.Now(), "http-shop-envelope", "shop_purchase"))
	if !purchase.OK || purchase.Receipt.ProductID != "chest.standard.pull" || purchase.Wallet["gold"] != 1200 {
		t.Fatalf("shop purchase route invalid: %+v", purchase)
	}
	rejected := postRawWithHeaders(t, server.URL+"/v1/shop/purchase", alice.SessionToken, map[string]any{
		"product_id": "card.focus_lens.single",
		"quantity":   0,
		"nonce":      "http-shop-invalid",
	}, businessEnvelopeHeaders(2, time.Now(), "http-shop-invalid-envelope", "shop_purchase"))
	if rejected.Code != http.StatusBadRequest || rejected.ErrorCode != "quantity_invalid" {
		t.Fatalf("shop quantity error invalid: %+v", rejected)
	}
	replayed := postRawWithHeaders(t, server.URL+"/v1/shop/purchase", alice.SessionToken, map[string]any{
		"product_id": "card.focus_lens.single",
		"quantity":   1,
		"nonce":      "http-shop-replay",
	}, businessEnvelopeHeaders(1, time.Now(), "http-shop-envelope", "shop_purchase"))
	if replayed.Code != http.StatusConflict || replayed.ErrorCode != security.CodeBusinessEnvelopeReplay {
		t.Fatalf("shop purchase should reject replayed business envelope: %+v", replayed)
	}
}
