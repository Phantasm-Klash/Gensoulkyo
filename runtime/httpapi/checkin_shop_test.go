package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"gensoulkyo/runtime/core"
)

func TestHTTPCheckinAndShopFlow(t *testing.T) {
	service := core.NewService(core.Config{})
	server := httptest.NewServer(New(service))
	defer server.Close()

	player := postJSON[core.AuthSession](t, server.URL+"/v1/auth/anonymous", "", map[string]any{"device_id": "checkin-a", "display_name": "Checkin A"})
	if player.SessionToken == "" {
		t.Fatalf("expected session token")
	}

	// 1) 初始签到状态：第 1 天可领。
	checkin := getJSON[core.CheckinView](t, server.URL+"/v1/checkin", player.SessionToken)
	if !checkin.OK || checkin.CycleID == "" || len(checkin.Days) != 7 {
		t.Fatalf("checkin view invalid: %+v", checkin)
	}
	if checkin.NextClaimableDay != 1 || checkin.Streak != 0 {
		t.Fatalf("expected day 1 claimable with streak 0, got %+v", checkin)
	}
	claimable := 0
	for _, day := range checkin.Days {
		if day.Claimable {
			claimable++
			if day.Day != 1 {
				t.Fatalf("expected day 1 claimable, got day %d", day.Day)
			}
		}
	}
	if claimable != 1 {
		t.Fatalf("expected exactly one claimable day, got %d", claimable)
	}

	// 2) 领取成功 → 钱包增加。
	before := getJSON[core.ShopView](t, server.URL+"/v1/shop", player.SessionToken)
	claim := postJSON[core.CheckinClaimView](t, server.URL+"/v1/checkin/claim", player.SessionToken, map[string]any{})
	if !claim.OK || claim.Day != 1 || claim.Streak != 1 {
		t.Fatalf("checkin claim invalid: %+v", claim)
	}
	if claim.Wallet["gold"] != before.Wallet["gold"]+claim.Reward.Gold {
		t.Fatalf("wallet gold not increased by reward: %+v vs before %+v", claim.Wallet, before.Wallet)
	}
	if claim.NextClaimableDay != 2 {
		t.Fatalf("expected next claimable day 2, got %d", claim.NextClaimableDay)
	}

	// 3) 再领 → already_claimed。
	duplicate := postRaw(t, server.URL+"/v1/checkin/claim", player.SessionToken, map[string]any{})
	if duplicate.Code != http.StatusBadRequest || duplicate.ErrorCode != "already_claimed" {
		t.Fatalf("expected already_claimed, got %+v", duplicate)
	}

	// 4) 商城列表返回钱包与商品。
	shop := getJSON[core.ShopView](t, server.URL+"/v1/shop", player.SessionToken)
	if !shop.OK || shop.Currency != "gold" || len(shop.Items) == 0 {
		t.Fatalf("shop view invalid: %+v", shop)
	}
	if shop.Wallet["gold"] != claim.Wallet["gold"] {
		t.Fatalf("shop wallet mismatch: %+v vs %+v", shop.Wallet, claim.Wallet)
	}
	var potion core.ShopItemView
	found := false
	for _, item := range shop.Items {
		if item.ItemID == "stamina_potion" {
			potion = item
			found = true
		}
	}
	if !found || potion.Stock != -1 || potion.Price["gold"] <= 0 || !potion.Purchasable {
		t.Fatalf("stamina_potion item invalid: %+v", potion)
	}

	// 5) 购买成功 → 扣钱 + 加库存。
	buy := postJSON[core.ShopPurchaseView](t, server.URL+"/v1/shop/purchase", player.SessionToken, map[string]any{"item_id": "stamina_potion", "count": 1})
	if !buy.OK || buy.ItemID != "stamina_potion" || buy.Count != 1 {
		t.Fatalf("purchase invalid: %+v", buy)
	}
	if buy.Spent["gold"] != potion.Price["gold"] {
		t.Fatalf("spent mismatch: %+v", buy.Spent)
	}
	if buy.Wallet["gold"] != shop.Wallet["gold"]-potion.Price["gold"] {
		t.Fatalf("wallet not debited: %+v vs %+v", buy.Wallet, shop.Wallet)
	}
	if buy.Inventory["stamina_potion"] != 1 {
		t.Fatalf("inventory not granted: %+v", buy.Inventory)
	}

	// 6) 余额不足 → insufficient_funds。
	shopAfter := getJSON[core.ShopView](t, server.URL+"/v1/shop", player.SessionToken)
	drain := shopAfter.Wallet["gold"]/potion.Price["gold"] + 1
	insufficient := postRaw(t, server.URL+"/v1/shop/purchase", player.SessionToken, map[string]any{"item_id": "stamina_potion", "count": drain})
	if insufficient.Code != http.StatusBadRequest || insufficient.ErrorCode != "insufficient_funds" {
		t.Fatalf("expected insufficient_funds, got %+v", insufficient)
	}

	// 7) 不存在的商品 → not_found。
	missing := postRaw(t, server.URL+"/v1/shop/purchase", player.SessionToken, map[string]any{"item_id": "no_such_item", "count": 1})
	if missing.Code != http.StatusNotFound || missing.ErrorCode != "not_found" {
		t.Fatalf("expected not_found, got %+v", missing)
	}

	// 8) 未授权 → unauthorized。
	unauthorized := postRaw(t, server.URL+"/v1/shop/purchase", "", map[string]any{"item_id": "stamina_potion", "count": 1})
	if unauthorized.Code != http.StatusUnauthorized || unauthorized.ErrorCode != "unauthorized" {
		t.Fatalf("expected unauthorized, got %+v", unauthorized)
	}
}
