package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"gensoulkyo/runtime/core"
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
	purchase := postJSON[core.ShopPurchaseResponse](t, server.URL+"/v1/shop/purchase", alice.SessionToken, map[string]any{
		"product_id": "chest.standard.pull",
		"quantity":   1,
		"nonce":      "http-shop-nonce",
	})
	if !purchase.OK || purchase.Receipt.ProductID != "chest.standard.pull" || purchase.Wallet["gold"] != 1200 {
		t.Fatalf("shop purchase route invalid: %+v", purchase)
	}
	rejected := postRaw(t, server.URL+"/v1/shop/purchase", alice.SessionToken, map[string]any{
		"product_id": "card.focus_lens.single",
		"quantity":   0,
		"nonce":      "http-shop-invalid",
	})
	if rejected.Code != http.StatusBadRequest || rejected.ErrorCode != "quantity_invalid" {
		t.Fatalf("shop quantity error invalid: %+v", rejected)
	}
}
