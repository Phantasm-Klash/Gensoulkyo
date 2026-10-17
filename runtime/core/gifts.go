package core

// In-match gift catalog. A gift is a social interaction, not a simulation
// effect: the sender spends energy, the lobby appends one gift_sent event to
// the match event stream, and the deterministic simulation is never touched —
// that is what keeps a .dem replay byte-identical.
//
// The ids, names and costs mirror the client catalog (SpellKard laya
// src/core/game/gift_catalog.ts) one-for-one. The server keeps its own copy so
// a gift can be validated and priced without trusting the client; an id is
// echoed back in gift_sent events, so renaming one is a protocol change.
type giftDefinition struct {
	GiftID string
	Name   string
	// Cost is the energy the sender spends, in the same units as
	// playerState.Energy (the card economy scale).
	Cost float64
}

// giftCatalog is the full server-side gift table keyed by wire id. Pure
// compile-time data: like the card catalog it never enters any serialized
// (hashed) state — only the gift_sent event a gift actually emits does, and
// that event lives on the social event stream, not in the simulation.
var giftCatalog = map[string]giftDefinition{
	"heart":   {GiftID: "heart", Name: "心愿", Cost: 10},
	"star":    {GiftID: "star", Name: "星砂", Cost: 20},
	"ribbon":  {GiftID: "ribbon", Name: "丝带", Cost: 30},
	"lantern": {GiftID: "lantern", Name: "灯笼", Cost: 40},
	"wand":    {GiftID: "wand", Name: "御币", Cost: 50},
	"cup":     {GiftID: "cup", Name: "茶", Cost: 60},
}

// giftDefinitionFor resolves a gift id. An unknown id is not an error here —
// the caller decides the rejection — so ids added later stay a pure lookup.
func giftDefinitionFor(giftID string) (giftDefinition, bool) {
	definition, ok := giftCatalog[giftID]
	return definition, ok
}
