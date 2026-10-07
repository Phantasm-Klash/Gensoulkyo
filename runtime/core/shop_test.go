package core

import (
	"testing"
	"time"
)

func TestShopCatalogAndPurchaseAreServerAuthoritative(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	service := NewService(Config{Clock: func() time.Time { return now }})
	alice := mustLogin(t, service, "Shop Alice")

	service.mu.Lock()
	service.users[alice.UserID].Inventory["focus_lens"] = CardInventoryEntry{
		CardID:          "focus_lens",
		FirstObtainedAt: now,
	}
	service.mu.Unlock()

	catalog, err := service.ShopCatalog(alice.SessionToken)
	if err != nil {
		t.Fatalf("shop catalog: %v", err)
	}
	if !catalog.OK || len(catalog.Products) != len(serverShopCatalog) || catalog.Season != shopCatalogSeason || catalog.Wallet["gold"] != 2000 || catalog.ServerTime != now.UnixMilli() {
		t.Fatalf("shop catalog invalid: %+v", catalog)
	}
	for index := 1; index < len(catalog.Products); index++ {
		if catalog.Products[index-1].ProductID >= catalog.Products[index].ProductID {
			t.Fatalf("shop catalog must be sorted and stable: %+v", catalog.Products)
		}
	}

	purchased, err := service.PurchaseShopProduct(alice.SessionToken, ShopPurchaseRequest{
		ProductID: "card.focus_lens.single",
		Quantity:  1,
		Nonce:     "shop-nonce-1",
	})
	if err != nil {
		t.Fatalf("shop purchase: %v", err)
	}
	if !purchased.OK || !purchased.ServerAuthoritative || purchased.Wallet["gold"] != 1800 || purchased.Receipt.ReceiptID == "" {
		t.Fatalf("shop purchase invalid: %+v", purchased)
	}
	var focusCopies int
	for _, item := range purchased.Inventory.Items {
		if item.CardID == "focus_lens" {
			focusCopies = item.Copies
		}
	}
	if focusCopies != 1 {
		t.Fatalf("shop purchase should grant the selected card: %+v", purchased.Inventory)
	}
	if len(purchased.Granted) != 1 || purchased.Granted[0].Type != "card" || purchased.Granted[0].ItemID != "focus_lens" || purchased.Granted[0].Quantity != 1 {
		t.Fatalf("shop grant invalid: %+v", purchased.Granted)
	}

	duplicate, err := service.PurchaseShopProduct(alice.SessionToken, ShopPurchaseRequest{
		ProductID: "card.focus_lens.single",
		Quantity:  1,
		Nonce:     "shop-nonce-1",
	})
	if err != nil {
		t.Fatalf("duplicate shop purchase: %v", err)
	}
	if duplicate.Receipt != purchased.Receipt || duplicate.Wallet["gold"] != 1800 {
		t.Fatalf("duplicate purchase must reuse receipt and wallet: first=%+v duplicate=%+v", purchased, duplicate)
	}
	if _, err := service.PurchaseShopProduct(alice.SessionToken, ShopPurchaseRequest{
		ProductID: "card.last_arc.single",
		Quantity:  1,
		Nonce:     "shop-nonce-1",
	}); ErrorCode(err) != codeIdempotencyConflict {
		t.Fatalf("expected idempotency conflict, got %v", err)
	}
}

func TestShopPurchaseRejectsWithoutMutation(t *testing.T) {
	service := NewService(Config{})
	alice := mustLogin(t, service, "Shop Insufficient")
	service.mu.Lock()
	service.users[alice.UserID].Wallet["gold"] = 0
	beforeWallet := copyIntMap(service.users[alice.UserID].Wallet)
	beforeInventory := copyShopInventory(service.users[alice.UserID].Inventory)
	service.mu.Unlock()

	if _, err := service.PurchaseShopProduct(alice.SessionToken, ShopPurchaseRequest{
		ProductID: "card.focus_lens.single",
		Quantity:  1,
		Nonce:     "shop-insufficient",
	}); ErrorCode(err) != codeInsufficientCurrency {
		t.Fatalf("expected insufficient currency, got %v", err)
	}
	if _, err := service.PurchaseShopProduct(alice.SessionToken, ShopPurchaseRequest{
		ProductID: "shop.missing",
		Quantity:  1,
		Nonce:     "shop-missing",
	}); ErrorCode(err) != codeProductNotFound {
		t.Fatalf("expected product not found, got %v", err)
	}
	if _, err := service.PurchaseShopProduct(alice.SessionToken, ShopPurchaseRequest{
		ProductID: "card.focus_lens.single",
		Quantity:  maxShopPurchaseQuantity + 1,
		Nonce:     "shop-quantity",
	}); ErrorCode(err) != codeQuantityInvalid {
		t.Fatalf("expected quantity invalid, got %v", err)
	}

	service.mu.Lock()
	after := service.users[alice.UserID]
	afterWallet := copyIntMap(after.Wallet)
	afterInventory := copyShopInventory(after.Inventory)
	service.mu.Unlock()
	if len(beforeWallet) != len(afterWallet) || len(beforeInventory) != len(afterInventory) {
		t.Fatalf("rejected purchases changed wallet or inventory: before=%+v after=%+v", beforeWallet, afterWallet)
	}
	for key, value := range beforeWallet {
		if afterWallet[key] != value {
			t.Fatalf("wallet changed for %s: before=%d after=%d", key, value, afterWallet[key])
		}
	}
	for key, value := range beforeInventory {
		if afterInventory[key] != value {
			t.Fatalf("inventory changed for %s: before=%+v after=%+v", key, value, afterInventory[key])
		}
	}
}

func copyShopInventory(source map[string]CardInventoryEntry) map[string]CardInventoryEntry {
	out := make(map[string]CardInventoryEntry, len(source))
	for key, value := range source {
		out[key] = value
	}
	return out
}

func TestShopOperationsAreRPCOnlyClientContracts(t *testing.T) {
	if !stringSliceContains(ContractClientOperations(), "shop.catalog") || !stringSliceContains(ContractClientOperations(), "shop.purchase") {
		t.Fatalf("shop operations missing from client contract: %+v", ContractClientOperations())
	}
	if !stringSliceContains(ContractClientRPCOperations(), "shop.catalog") || !stringSliceContains(ContractClientRPCOperations(), "shop.purchase") {
		t.Fatalf("shop operations missing from RPC contract: %+v", ContractClientRPCOperations())
	}
	if stringSliceContains(ContractClientWSSOperations(), "shop.catalog") || stringSliceContains(ContractClientWSSOperations(), "shop.purchase") {
		t.Fatalf("shop purchase/catalog must not be advertised as WSS: %+v", ContractClientWSSOperations())
	}
	contracts := ContractClientOperationContracts()
	for _, contract := range contracts {
		if contract.Operation == "shop.catalog" && len(contract.ClientRequestFields) != 0 {
			t.Fatalf("shop catalog should be bodyless: %+v", contract)
		}
		if contract.Operation == "shop.purchase" {
			for _, field := range []string{"product_id", "quantity", "nonce"} {
				if !stringSliceContains(contract.ClientRequestFields, field) {
					t.Fatalf("shop purchase missing request field %q: %+v", field, contract)
				}
			}
		}
	}
}
