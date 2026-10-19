package httpapi

// gacha_http_test.go: transport-level acceptance for the T10 gacha routes.
// Exercises route → handler → core.Service → JSON for all four endpoints.
// The faith-exchange *success* path needs 150 faith (150 pulls), which is a
// core-level test (runtime/core/gacha_test.go); here the route is proven by
// its validation path plus the roster/pull round trips.

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"gensoulkyo/runtime/core"
)

func TestHTTPGachaPoolsRosterPullRoutes(t *testing.T) {
	service := core.NewService(core.Config{})
	server := httptest.NewServer(New(service))
	defer server.Close()

	alice := postJSON[core.AuthSession](t, server.URL+"/v1/auth/anonymous", "", map[string]any{
		"device_id":    "gacha-http",
		"display_name": "Gacha HTTP",
	})

	// --- GET /v1/gacha/pools ---
	pools := getJSON[core.GachaPoolsResponse](t, server.URL+"/v1/gacha/pools", alice.SessionToken)
	if !pools.OK || len(pools.Pools) != 2 {
		t.Fatalf("gacha pools route invalid: %+v", pools)
	}
	seen := map[string]bool{}
	for _, pool := range pools.Pools {
		seen[pool.PoolID] = true
		if pool.CostGems != 200 || pool.CostTenGems != 1800 {
			t.Fatalf("pool %s costs = %d/%d, want 200/1800", pool.PoolID, pool.CostGems, pool.CostTenGems)
		}
	}
	if !seen["gacha.cards"] || !seen["gacha.characters"] {
		t.Fatalf("pools = %+v, want both totems", seen)
	}

	// --- GET /v1/gacha/roster (starter kit) ---
	roster := getJSON[core.CharacterRosterSnapshot](t, server.URL+"/v1/gacha/roster", alice.SessionToken)
	if len(roster.Shots) != 2 || roster.Shots[0].ShotID != "ReimuA1" || roster.Shots[1].ShotID != "ReimuA2" {
		t.Fatalf("starter roster = %+v, want ReimuA1+ReimuA2", roster.Shots)
	}
	if len(roster.Bombs) != 1 || roster.Bombs[0] != "Reimu" {
		t.Fatalf("starter bombs = %+v, want [Reimu]", roster.Bombs)
	}
	if len(roster.LockedShotIDs) != 50 {
		t.Fatalf("locked shots = %d, want 50", len(roster.LockedShotIDs))
	}

	// --- POST /v1/gacha/pull without a business envelope is refused ---
	missingEnvelope := postRaw(t, server.URL+"/v1/gacha/pull", alice.SessionToken, map[string]any{
		"pool_id": "gacha.characters",
		"count":   1,
	})
	if missingEnvelope.Code != http.StatusBadRequest || missingEnvelope.ErrorCode != "business_envelope_required" {
		t.Fatalf("pull without envelope = %+v, want business_envelope_required", missingEnvelope)
	}

	// --- close the economy loop: gold -> gems (the only gem source) ---
	for seq := 1; seq <= 3; seq++ {
		purchase := postJSONWithHeaders[core.ShopPurchaseResponse](t, server.URL+"/v1/shop/purchase", alice.SessionToken, map[string]any{
			"product_id": "gems.pack.600",
			"quantity":   1,
			"nonce":      "gacha-gems-" + string(rune('0'+seq)),
		}, businessEnvelopeHeaders(int64(seq), time.Now(), "gacha-gems-envelope-"+string(rune('0'+seq)), "shop_purchase"))
		if !purchase.OK {
			t.Fatalf("gems purchase %d invalid: %+v", seq, purchase)
		}
	}
	if purchaseWallet := getJSON[core.ShopCatalogResponse](t, server.URL+"/v1/shop/catalog", alice.SessionToken); purchaseWallet.Wallet["gems"] != 2100 {
		t.Fatalf("gems after 3 packs = %d, want 2100", purchaseWallet.Wallet["gems"])
	}

	// --- POST /v1/gacha/pull with the envelope ---
	pull := postJSONWithHeaders[core.GachaPullResponse](t, server.URL+"/v1/gacha/pull", alice.SessionToken, map[string]any{
		"pool_id": "gacha.characters",
		"count":   1,
	}, businessEnvelopeHeaders(4, time.Now(), "http-gacha-pull", "gacha_pull"))
	if !pull.OK || len(pull.Results) != 1 {
		t.Fatalf("single pull invalid: %+v", pull)
	}
	result := pull.Results[0]
	if result.Kind != "character" || result.ShotID == "" {
		t.Fatalf("single pull result = %+v, want a character draw", result)
	}
	if pull.Wallet["gems"] != 1900 {
		t.Fatalf("gems after single = %d, want 1900", pull.Wallet["gems"])
	}
	if pull.Faith != 1 {
		t.Fatalf("faith after single = %d, want 1", pull.Faith)
	}
	owned := false
	for _, shot := range pull.Characters.Shots {
		if shot.ShotID == result.ShotID {
			owned = true
		}
	}
	if !owned {
		t.Fatalf("pull result %q missing from the returned roster", result.ShotID)
	}

	// --- ten-pull: 10 results, 1800 gems, rare+ guaranteed ---
	ten := postJSONWithHeaders[core.GachaPullResponse](t, server.URL+"/v1/gacha/pull", alice.SessionToken, map[string]any{
		"pool_id": "gacha.cards",
		"count":   10,
	}, businessEnvelopeHeaders(5, time.Now(), "http-gacha-ten", "gacha_pull"))
	if !ten.OK || len(ten.Results) != 10 {
		t.Fatalf("ten pull invalid: %+v", ten)
	}
	if ten.Wallet["gems"] != 100 {
		t.Fatalf("gems after ten = %d, want 100", ten.Wallet["gems"])
	}
	if ten.Faith != 11 {
		t.Fatalf("faith after ten = %d, want 11 (shared across pools)", ten.Faith)
	}
	best := 0
	for _, item := range ten.Results {
		if rank := gachaTestRarityRank(item.Rarity); rank > best {
			best = rank
		}
	}
	if best < 2 {
		t.Fatalf("ten pull must contain a rare+ result: %+v", ten.Results)
	}

	// --- invalid pool / invalid count are rejected with coded errors ---
	badPool := postRawWithHeaders(t, server.URL+"/v1/gacha/pull", alice.SessionToken, map[string]any{
		"pool_id": "gacha.nope",
		"count":   1,
	}, businessEnvelopeHeaders(6, time.Now(), "http-gacha-badpool", "gacha_pull"))
	if badPool.Code != http.StatusNotFound || badPool.ErrorCode != "not_found" {
		t.Fatalf("unknown pool = %+v, want not_found", badPool)
	}
	badCount := postRawWithHeaders(t, server.URL+"/v1/gacha/pull", alice.SessionToken, map[string]any{
		"pool_id": "gacha.cards",
		"count":   5,
	}, businessEnvelopeHeaders(7, time.Now(), "http-gacha-badcount", "gacha_pull"))
	if badCount.Code != http.StatusBadRequest || badCount.ErrorCode != "invalid_request" {
		t.Fatalf("bad count = %+v, want invalid_request", badCount)
	}
}

func TestHTTPGachaFaithExchangeValidation(t *testing.T) {
	service := core.NewService(core.Config{})
	server := httptest.NewServer(New(service))
	defer server.Close()

	alice := postJSON[core.AuthSession](t, server.URL+"/v1/auth/anonymous", "", map[string]any{
		"device_id":    "gacha-exchange-http",
		"display_name": "Gacha Exchange HTTP",
	})

	// A fresh identity has 0 faith: the route must answer insufficient_funds.
	poor := postRawWithHeaders(t, server.URL+"/v1/gacha/faith-exchange", alice.SessionToken, map[string]any{
		"item_id": "MarisaB2",
	}, businessEnvelopeHeaders(1, time.Now(), "http-gacha-poor", "gacha_faith_exchange"))
	if poor.Code != http.StatusBadRequest || poor.ErrorCode != "insufficient_funds" {
		t.Fatalf("poor exchange = %+v, want insufficient_funds", poor)
	}

	// Unknown item id is a not_found, independent of the wallet.
	unknown := postRawWithHeaders(t, server.URL+"/v1/gacha/faith-exchange", alice.SessionToken, map[string]any{
		"item_id": "NotAShot",
	}, businessEnvelopeHeaders(2, time.Now(), "http-gacha-unknown", "gacha_faith_exchange"))
	if unknown.Code != http.StatusNotFound || unknown.ErrorCode != "not_found" {
		t.Fatalf("unknown item = %+v, want not_found", unknown)
	}

	// An already-owned starter shot cannot be exchanged either.
	owned := postRawWithHeaders(t, server.URL+"/v1/gacha/faith-exchange", alice.SessionToken, map[string]any{
		"item_id": "ReimuA1",
	}, businessEnvelopeHeaders(3, time.Now(), "http-gacha-owned", "gacha_faith_exchange"))
	if owned.Code != http.StatusBadRequest || owned.ErrorCode != "already_claimed" {
		t.Fatalf("owned exchange = %+v, want already_claimed", owned)
	}
}

func gachaTestRarityRank(rarity string) int {
	switch rarity {
	case "epic":
		return 3
	case "rare":
		return 2
	case "uncommon":
		return 1
	default:
		return 0
	}
}
