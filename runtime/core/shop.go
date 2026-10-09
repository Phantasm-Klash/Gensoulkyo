package core

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	maxShopPurchaseQuantity = 10
	shopCatalogSeason       = defaultSeasonID
	shopCatalogVersion      = "shop-local-s0"
)

// WalletSnapshot is the server-owned currency projection returned by economy
// and shop operations.
type WalletSnapshot map[string]int

type ServerShopProduct struct {
	ProductID   string `json:"product_id"`
	Kind        string `json:"kind"`
	CostKind    string `json:"cost_kind"`
	CostAmount  int    `json:"cost_amount"`
	Payload     string `json:"payload"`
	Quantity    int    `json:"quantity"`
	Rarity      string `json:"rarity"`
	Season      string `json:"season"`
	DailyLimit  int    `json:"daily_limit"`
	Purchasable bool   `json:"purchasable"`
}

type ShopCatalogResponse struct {
	OK                  bool                `json:"ok"`
	Products            []ServerShopProduct `json:"products"`
	Wallet              WalletSnapshot      `json:"wallet"`
	Season              string              `json:"season"`
	CatalogVersion      string              `json:"catalog_version"`
	ServerAuthoritative bool                `json:"server_authoritative"`
	ReadSource          string              `json:"read_source"`
	ServerTime          int64               `json:"server_time"`
	ServerTimeMS        int64               `json:"server_time_ms"`
}

type ShopPurchaseRequest struct {
	ProductID      string `json:"product_id"`
	Quantity       int    `json:"quantity"`
	Nonce          string `json:"nonce"`
	ItemID         string `json:"item_id,omitempty"`
	Count          int    `json:"count,omitempty"`
	CatalogVersion string `json:"catalog_version,omitempty"`
	IdempotencyKey string `json:"idempotency_key,omitempty"`
}

type ShopReceipt struct {
	ReceiptID      string `json:"receipt_id"`
	ProductID      string `json:"product_id"`
	ItemID         string `json:"item_id,omitempty"`
	Quantity       int    `json:"quantity"`
	CostKind       string `json:"cost_kind"`
	CostAmount     int    `json:"cost_amount"`
	LedgerID       string `json:"ledger_id"`
	CatalogVersion string `json:"catalog_version"`
	CreatedAt      int64  `json:"created_at"`
}

type GrantEntry struct {
	Type     string `json:"type"`
	ItemID   string `json:"item_id,omitempty"`
	Amount   int    `json:"amount"`
	Quantity int    `json:"quantity,omitempty"`
	Source   string `json:"source,omitempty"`
}

type ShopPurchaseResponse struct {
	OK                  bool              `json:"ok"`
	ProductID           string            `json:"product_id"`
	ItemID              string            `json:"item_id"`
	Quantity            int               `json:"quantity"`
	Count               int               `json:"count"`
	Spent               map[string]int    `json:"spent"`
	Wallet              WalletSnapshot    `json:"wallet"`
	Inventory           InventorySnapshot `json:"inventory"`
	Granted             []GrantEntry      `json:"granted"`
	Receipt             ShopReceipt       `json:"receipt"`
	LedgerID            string            `json:"ledger_id"`
	CatalogVersion      string            `json:"catalog_version"`
	Duplicate           bool              `json:"duplicate"`
	ServerAuthoritative bool              `json:"server_authoritative"`
	ServerTime          int64             `json:"server_time"`
	ServerTimeMS        int64             `json:"server_time_ms"`
}

var forbiddenShopPurchaseFields = map[string]struct{}{
	"price":                {},
	"cost":                 {},
	"cost_kind":            {},
	"cost_amount":          {},
	"rarity":               {},
	"payload":              {},
	"drop":                 {},
	"drops":                {},
	"grant":                {},
	"granted":              {},
	"reward":               {},
	"rewards":              {},
	"inventory":            {},
	"wallet":               {},
	"receipt":              {},
	"receipt_id":           {},
	"ledger_id":            {},
	"duplicate":            {},
	"server_authoritative": {},
	"server_time":          {},
	"server_time_ms":       {},
	"created_at":           {},
}

func ForbiddenShopPurchaseField(raw map[string]any) string {
	return firstForbiddenShopPurchaseField(raw)
}

func firstForbiddenShopPurchaseField(raw map[string]any) string {
	for key, value := range raw {
		if _, forbidden := forbiddenShopPurchaseFields[strings.ToLower(strings.TrimSpace(key))]; forbidden {
			return key
		}
		switch typed := value.(type) {
		case map[string]any:
			if nested := firstForbiddenShopPurchaseField(typed); nested != "" {
				return nested
			}
		case []any:
			for _, item := range typed {
				if nestedMap, ok := item.(map[string]any); ok {
					if nested := firstForbiddenShopPurchaseField(nestedMap); nested != "" {
						return nested
					}
				}
			}
		}
	}
	return ""
}

// serverShopCatalog is deliberately server-owned and deterministic. The
// client may only select a product id; price, rarity and payload come from
// this definition.
var serverShopCatalog = []ServerShopProduct{
	{ProductID: "card.focus_lens.single", Kind: "card", CostKind: "gold", CostAmount: 200, Payload: "focus_lens", Quantity: 1, Rarity: "common", Season: shopCatalogSeason, Purchasable: true},
	{ProductID: "card.bomb_amplifier.single", Kind: "card", CostKind: "gold", CostAmount: 200, Payload: "bomb_amplifier", Quantity: 1, Rarity: "common", Season: shopCatalogSeason, Purchasable: true},
	{ProductID: "card.purge_charm.single", Kind: "card", CostKind: "gold", CostAmount: 500, Payload: "purge_charm", Quantity: 1, Rarity: "uncommon", Season: shopCatalogSeason, Purchasable: true},
	{ProductID: "card.density_surge.single", Kind: "card", CostKind: "gold", CostAmount: 1200, Payload: "density_surge", Quantity: 1, Rarity: "rare", Season: shopCatalogSeason, Purchasable: true},
	{ProductID: "card.last_arc.single", Kind: "card", CostKind: "gems", CostAmount: 300, Payload: "last_arc", Quantity: 1, Rarity: "epic", Season: shopCatalogSeason, Purchasable: true},
	{ProductID: "chest.standard.pull", Kind: "chest", CostKind: "gold", CostAmount: 800, Payload: defaultChestPoolID, Quantity: 1, Season: shopCatalogSeason, Purchasable: true},
}

type shopPurchaseRecord struct {
	RequestHash string
	Response    ShopPurchaseResponse
}

func defaultWallet() map[string]int {
	return map[string]int{
		"points":     0,
		"gold":       2000,
		"gems":       300,
		"card_dust":  0,
		"chest_keys": 1,
	}
}

func (s *Service) ShopCatalog(sessionToken string) (*ShopCatalogResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	user, err := s.userBySessionLocked(sessionToken)
	if err != nil {
		return nil, err
	}
	now := s.clock()
	serverTimeMS := now.UnixMilli()
	return &ShopCatalogResponse{
		OK:                  true,
		Products:            copyShopProducts(serverShopCatalog),
		Wallet:              copyWallet(user.Wallet),
		Season:              shopCatalogSeason,
		CatalogVersion:      shopCatalogVersion,
		ServerAuthoritative: true,
		ReadSource:          "memory",
		ServerTime:          serverTimeMS,
		ServerTimeMS:        serverTimeMS,
	}, nil
}

func (s *Service) PurchaseShopProduct(sessionToken string, req ShopPurchaseRequest) (*ShopPurchaseResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	user, err := s.userBySessionLocked(sessionToken)
	if err != nil {
		return nil, err
	}
	productID, quantity, idempotencyKey, catalogVersion, err := normalizeShopPurchaseRequest(req)
	if err != nil {
		return nil, err
	}
	if catalogVersion != "" && catalogVersion != shopCatalogVersion {
		return nil, newError(codeCatalogVersionMismatch, "catalog version %q is not current", catalogVersion)
	}
	product, ok := shopProductByID(productID)
	if !ok {
		return nil, newError(codeProductNotFound, "shop product %q was not found", productID)
	}
	if !product.Purchasable {
		return nil, newError(codeNotPurchasable, "shop product %q is not purchasable", productID)
	}
	requestHash := shopPurchaseRequestHash(productID, quantity, catalogVersion)
	purchaseKey := user.UserID + "\x00" + idempotencyKey
	if record, ok := s.shopPurchases[purchaseKey]; ok {
		if record.RequestHash != requestHash {
			return nil, newError(codeIdempotencyConflict, "nonce was already used for a different purchase")
		}
		response := copyShopPurchaseResponse(record.Response)
		response.Wallet = copyWallet(user.Wallet)
		response.Inventory = s.inventorySnapshotLocked(user)
		response.Duplicate = true
		response.ServerTime = s.clock().UnixMilli()
		response.ServerTimeMS = response.ServerTime
		return &response, nil
	}

	totalCost := product.CostAmount * quantity
	if product.CostAmount <= 0 || totalCost/product.CostAmount != quantity {
		return nil, newError(codeInvalidRequest, "shop product %q has an invalid cost", productID)
	}
	if user.Wallet == nil {
		user.Wallet = defaultWallet()
	}
	if user.Wallet[product.CostKind] < totalCost {
		return nil, newError(codeInsufficientCurrency, "not enough %s", product.CostKind)
	}
	now := s.clock()
	limitKey := shopPurchaseLimitKey(user.UserID, productID, now)
	if product.DailyLimit > 0 && s.shopPurchaseLimits[limitKey]+quantity > product.DailyLimit {
		return nil, newError(codeDailyLimitReached, "daily limit reached for %s", productID)
	}

	user.Wallet[product.CostKind] -= totalCost
	granted := grantShopProductLocked(user, product, quantity, now)
	s.shopPurchaseLimits[limitKey] += quantity
	ledgerID := s.nextIDLocked("shop_ledger")
	receipt := ShopReceipt{
		ReceiptID:      s.nextIDLocked("shop_receipt"),
		ProductID:      productID,
		ItemID:         productID,
		Quantity:       quantity,
		CostKind:       product.CostKind,
		CostAmount:     totalCost,
		LedgerID:       ledgerID,
		CatalogVersion: shopCatalogVersion,
		CreatedAt:      now.UnixMilli(),
	}
	response := ShopPurchaseResponse{
		OK:                  true,
		ProductID:           productID,
		ItemID:              productID,
		Quantity:            quantity,
		Count:               quantity,
		Spent:               map[string]int{product.CostKind: totalCost},
		Wallet:              copyWallet(user.Wallet),
		Inventory:           s.inventorySnapshotLocked(user),
		Granted:             copyGrantEntries(granted),
		Receipt:             receipt,
		LedgerID:            ledgerID,
		CatalogVersion:      shopCatalogVersion,
		Duplicate:           false,
		ServerAuthoritative: true,
		ServerTime:          now.UnixMilli(),
		ServerTimeMS:        now.UnixMilli(),
	}
	s.shopPurchases[purchaseKey] = shopPurchaseRecord{
		RequestHash: requestHash,
		Response:    copyShopPurchaseResponse(response),
	}
	return &response, nil
}

func normalizeShopPurchaseRequest(req ShopPurchaseRequest) (string, int, string, string, error) {
	productID := strings.TrimSpace(req.ProductID)
	aliasProductID := strings.TrimSpace(req.ItemID)
	if productID == "" {
		productID = aliasProductID
	} else if aliasProductID != "" && aliasProductID != productID {
		return "", 0, "", "", newError(codeInvalidRequest, "product_id and item_id must match")
	}
	if productID == "" {
		return "", 0, "", "", newError(codeInvalidRequest, "product_id is required")
	}

	quantity := req.Quantity
	if quantity == 0 {
		quantity = req.Count
	} else if req.Count != 0 && req.Count != quantity {
		return "", 0, "", "", newError(codeInvalidRequest, "quantity and count must match")
	}
	if quantity <= 0 || quantity > maxShopPurchaseQuantity {
		return "", 0, "", "", newError(codeQuantityInvalid, "quantity must be between 1 and %d", maxShopPurchaseQuantity)
	}

	idempotencyKey := strings.TrimSpace(req.IdempotencyKey)
	nonce := strings.TrimSpace(req.Nonce)
	if idempotencyKey == "" {
		idempotencyKey = nonce
	} else if nonce != "" && nonce != idempotencyKey {
		return "", 0, "", "", newError(codeIdempotencyConflict, "nonce and idempotency_key must match")
	}
	if idempotencyKey == "" {
		return "", 0, "", "", newError(codeIdempotencyConflict, "nonce or idempotency_key is required")
	}
	catalogVersion := strings.TrimSpace(req.CatalogVersion)
	if catalogVersion == "" {
		catalogVersion = shopCatalogVersion
	}
	return productID, quantity, idempotencyKey, catalogVersion, nil
}

func shopProductByID(productID string) (ServerShopProduct, bool) {
	for _, product := range serverShopCatalog {
		if product.ProductID == productID {
			return product, true
		}
	}
	return ServerShopProduct{}, false
}

func grantShopProductLocked(user *userState, product ServerShopProduct, quantity int, now time.Time) []GrantEntry {
	grantQuantity := product.Quantity * quantity
	if grantQuantity <= 0 {
		return nil
	}
	switch product.Kind {
	case "card":
		accepted, overflow, dust := grantCardToUserLocked(user, product.Payload, grantQuantity, now)
		grants := make([]GrantEntry, 0, 2)
		if accepted > 0 {
			grants = append(grants, GrantEntry{Type: "card", ItemID: product.Payload, Amount: accepted, Quantity: accepted, Source: "shop_purchase"})
		}
		if overflow > 0 {
			grants = append(grants, GrantEntry{Type: "currency", ItemID: "card_dust", Amount: dust, Quantity: dust, Source: "shop_purchase_overflow"})
		}
		return grants
	case "chest":
		if user.Chests == nil {
			user.Chests = defaultChests()
		}
		user.Chests[product.Payload] += grantQuantity
		return []GrantEntry{{Type: "chest", ItemID: product.Payload, Amount: grantQuantity, Quantity: grantQuantity, Source: "shop_purchase"}}
	case "currency_bundle":
		user.Wallet[product.Payload] += grantQuantity
		return []GrantEntry{{Type: "currency", ItemID: product.Payload, Amount: grantQuantity, Quantity: grantQuantity, Source: "shop_purchase"}}
	default:
		return nil
	}
}

func shopPurchaseRequestHash(productID string, quantity int, catalogVersion string) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s:%d:%s", productID, quantity, catalogVersion)))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func shopPurchaseLimitKey(userID, productID string, now time.Time) string {
	return userID + "\x00" + productID + "\x00" + now.UTC().Format("2006-01-02")
}

func copyShopProducts(source []ServerShopProduct) []ServerShopProduct {
	out := append([]ServerShopProduct(nil), source...)
	sort.Slice(out, func(i, j int) bool { return out[i].ProductID < out[j].ProductID })
	return out
}

func copyWallet(source map[string]int) WalletSnapshot {
	out := make(WalletSnapshot, len(source))
	for key, value := range source {
		out[key] = value
	}
	return out
}

func copyGrantEntries(source []GrantEntry) []GrantEntry {
	return append([]GrantEntry(nil), source...)
}

func copyShopPurchaseResponse(source ShopPurchaseResponse) ShopPurchaseResponse {
	out := source
	out.Wallet = copyWallet(source.Wallet)
	out.Inventory = source.Inventory
	out.Inventory.Items = append([]CardInventoryEntry(nil), source.Inventory.Items...)
	out.Spent = copyWallet(source.Spent)
	out.Granted = copyGrantEntries(source.Granted)
	return out
}
