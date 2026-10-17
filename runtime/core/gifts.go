package core

// In-match gift catalog. A gift is a social interaction, not a simulation
// effect: the sender spends gift charges, the lobby appends one gift_sent
// event to the match event stream, and the deterministic simulation is never
// touched — that is what keeps a .dem replay byte-identical.
//
// The ids and names mirror the client catalog (SpellKard laya
// src/core/game/gift_catalog.ts) one-for-one. The server keeps its own copy so
// a gift can be validated and priced without trusting the client; an id is
// echoed back in gift_sent events, so renaming one is a protocol change.
//
// Charges are a SEPARATE pool from the card energy on purpose. The card
// economy runs on the ticket's `initial_energy` (2.0, cards up to 10.0), so
// pricing gifts in that unit made everything but the cheapest gift permanently
// unaffordable; a social interaction must never compete with the gameplay one.
type giftDefinition struct {
	GiftID string
	Name   string
	// Cost is the number of gift charges the sender spends. See
	// giftChargeBudget for the pool size.
	Cost int
}

// giftChargeBudget is how many gift charges each player gets per match. It caps
// the social interaction (anti-spam) without a timer: sending the whole catalog
// once costs 10 charges against a 5-charge budget, so the pool is a real
// choice, not a formality.
const giftChargeBudget = 5

// giftCatalog is the full server-side gift table keyed by wire id. Pure
// compile-time data: like the card catalog it never enters any serialized
// (hashed) state — only the gift_sent event a gift actually emits does, and
// that event lives on the social event stream, not in the simulation.
var giftCatalog = map[string]giftDefinition{
	"heart":   {GiftID: "heart", Name: "心愿", Cost: 1},
	"star":    {GiftID: "star", Name: "星砂", Cost: 1},
	"ribbon":  {GiftID: "ribbon", Name: "丝带", Cost: 1},
	"lantern": {GiftID: "lantern", Name: "灯笼", Cost: 2},
	"wand":    {GiftID: "wand", Name: "御币", Cost: 2},
	"cup":     {GiftID: "cup", Name: "茶", Cost: 3},
}

// giftDefinitionFor resolves a gift id. An unknown id is not an error here —
// the caller decides the rejection — so ids added later stay a pure lookup.
func giftDefinitionFor(giftID string) (giftDefinition, bool) {
	definition, ok := giftCatalog[giftID]
	return definition, ok
}
