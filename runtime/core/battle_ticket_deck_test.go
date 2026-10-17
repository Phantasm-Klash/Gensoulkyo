package core

import (
	"fmt"
	"strings"
	"testing"
)

// W-1 phase 2: the signed ticket carries the lobby's authoritative ordered
// deck; the opening hand must be its first len(hand) entries and every entry
// must be a catalog card id within the deck limit.
func TestBattleTicketIssuesDeckWithHandPrefix(t *testing.T) {
	service := NewService(Config{})
	alice := mustLogin(t, service, "Deck Alice")
	bob := mustLogin(t, service, "Deck Bob")
	matchID := matchTwoPlayers(t, service, alice, bob, "pvp_duel")

	ticket, err := service.BattleTicket(alice.SessionToken, matchID)
	if err != nil {
		t.Fatalf("battle ticket: %v", err)
	}
	service.mu.Lock()
	match := service.matches[matchID]
	service.mu.Unlock()
	if match == nil {
		t.Fatal("match missing after queue fill")
	}
	player := match.Players[alice.UserID]
	if player == nil {
		t.Fatal("player missing after queue fill")
	}
	// The ticket deck is exactly the deck the in-match draw consumes.
	wantDeck := make([]string, 0, len(player.DeckSnapshot.CardIDs))
	for _, cardID := range player.DeckSnapshot.CardIDs {
		wantDeck = append(wantDeck, strings.TrimSpace(cardID))
	}
	if len(ticket.Ticket.DeckCardIDs) != len(wantDeck) {
		t.Fatalf("deck = %v, want %v", ticket.Ticket.DeckCardIDs, wantDeck)
	}
	for index, cardID := range wantDeck {
		if ticket.Ticket.DeckCardIDs[index] != cardID {
			t.Fatalf("deck[%d] = %q, want %q", index, ticket.Ticket.DeckCardIDs[index], cardID)
		}
	}
	if len(ticket.Ticket.DeckCardIDs) > deckSize {
		t.Fatalf("deck size %d exceeds limit %d", len(ticket.Ticket.DeckCardIDs), deckSize)
	}
	for _, cardID := range ticket.Ticket.DeckCardIDs {
		if _, ok := serverCardCatalog[cardID]; !ok {
			t.Fatalf("deck card %q is not in the server catalog", cardID)
		}
	}
	// The opening hand is the deck's first len(hand) entries.
	hand := ticket.Ticket.HandCardIDs
	if len(hand) > len(ticket.Ticket.DeckCardIDs) {
		t.Fatalf("hand %v longer than deck %v", hand, ticket.Ticket.DeckCardIDs)
	}
	for index, cardID := range hand {
		if ticket.Ticket.DeckCardIDs[index] != cardID {
			t.Fatalf("hand[%d] = %q, want deck prefix %q", index, cardID, ticket.Ticket.DeckCardIDs[index])
		}
	}
}

// W-1 phase 2: the issuance hop refuses any deck that violates the wire
// contract (over the deckSize limit, unknown card, empty id, or a hand that
// is not the deck prefix) — the bound is re-checked, not inherited from the
// deck save hop.
func TestBattleTicketDeckValidationAtIssuance(t *testing.T) {
	base := validDeck("issuance_deck")

	oversized := base
	oversized.CardIDs = append(copyStringSlice(base.CardIDs), base.CardIDs[0])
	if _, err := battleTicketDeckCardIDs(oversized, initialHandCardIDs(oversized)); ErrorCode(err) != codeInvalidDeck {
		t.Fatalf("oversized deck must be refused, got %v", err)
	}

	unknown := base
	unknownIDs := copyStringSlice(base.CardIDs)
	unknownIDs[0] = "no_such_card"
	unknown.CardIDs = unknownIDs
	if _, err := battleTicketDeckCardIDs(unknown, nil); ErrorCode(err) != codeInvalidDeck {
		t.Fatalf("unknown card deck must be refused, got %v", err)
	}

	emptyID := base
	emptyIDs := copyStringSlice(base.CardIDs)
	emptyIDs[3] = "   "
	emptyID.CardIDs = emptyIDs
	if _, err := battleTicketDeckCardIDs(emptyID, nil); ErrorCode(err) != codeInvalidDeck {
		t.Fatalf("empty card id deck must be refused, got %v", err)
	}

	noDeck := base
	noDeck.CardIDs = nil
	if _, err := battleTicketDeckCardIDs(noDeck, nil); ErrorCode(err) != codeInvalidDeck {
		t.Fatalf("empty deck must be refused, got %v", err)
	}

	// A hand that is not the deck prefix cannot be issued (aegis_ward is a
	// catalog card that is not the deck's first entry).
	if _, err := battleTicketDeckCardIDs(base, []string{"aegis_ward"}); ErrorCode(err) != codeInvalidDeck {
		t.Fatalf("non-prefix hand must be refused, got %v", err)
	}

	resolved, err := battleTicketDeckCardIDs(base, initialHandCardIDs(base))
	if err != nil {
		t.Fatalf("valid deck refused: %v", err)
	}
	if len(resolved) != deckSize || resolved[0] != base.CardIDs[0] {
		t.Fatalf("resolved deck = %v", resolved)
	}
	hand := initialHandCardIDs(base)
	for index, cardID := range hand {
		if resolved[index] != cardID {
			t.Fatalf("resolved deck is not the hand prefix at %d: %q != %q", index, resolved[index], cardID)
		}
	}
}

// W-1 phase 2: a deck longer than the limit is refused at save time AND at
// ticket issuance when injected past the save hop (defense in depth on the
// hop that puts the deck on the signed wire).
func TestBattleTicketRefusesOversizedDeckEndToEnd(t *testing.T) {
	service := NewService(Config{})
	alice := mustLogin(t, service, "Oversize Alice")
	bob := mustLogin(t, service, "Oversize Bob")
	matchID := matchTwoPlayers(t, service, alice, bob, "pvp_duel")

	// Save-time refusal: 21 cards must not enter a deck record.
	if _, err := service.SaveDeck(alice.SessionToken, SaveDeckRequest{
		DeckID:  "too_long",
		Name:    "Too Long",
		Format:  defaultDeckFormat,
		CardIDs: append(copyStringSlice(validDeck("too_long").CardIDs), "focus_lens"),
		Active:  false,
	}); ErrorCode(err) != codeInvalidDeck {
		t.Fatalf("save must refuse a 21-card deck, got %v", err)
	}

	// Issuance-time refusal: inject the oversized deck past the save hop.
	service.mu.Lock()
	match := service.matches[matchID]
	if match == nil {
		service.mu.Unlock()
		t.Fatal("match missing after queue fill")
	}
	player := match.Players[alice.UserID]
	if player == nil {
		service.mu.Unlock()
		t.Fatal("player missing after queue fill")
	}
	player.DeckSnapshot.CardIDs = append(copyStringSlice(player.DeckSnapshot.CardIDs), player.DeckSnapshot.CardIDs[0])
	service.mu.Unlock()

	if _, err := service.BattleTicket(alice.SessionToken, matchID); ErrorCode(err) != codeInvalidDeck {
		t.Fatalf("ticket issuance must refuse an oversized deck, got %v", err)
	}
}

// W-1 phase 2: the deck rides inside the ED25519 signature cover — tampering
// with deck_card_ids must invalidate the ticket signature.
func TestBattleTicketDeckTamperBreaksSignature(t *testing.T) {
	service := NewService(Config{})
	alice := mustLogin(t, service, "Deck Tamper Alice")
	bob := mustLogin(t, service, "Deck Tamper Bob")
	matchID := matchTwoPlayers(t, service, alice, bob, "pvp_duel")

	ticket, err := service.BattleTicket(alice.SessionToken, matchID)
	if err != nil {
		t.Fatalf("battle ticket: %v", err)
	}
	if len(ticket.Ticket.DeckCardIDs) == 0 {
		t.Fatal("issued ticket must carry the deck")
	}
	if !verifySignedBattleTicket(t, ticket) {
		t.Fatal("issued ticket signature must verify over the marshaled ticket JSON")
	}
	tampered := *ticket
	tampered.Ticket.DeckCardIDs = []string{"finale_arc", "finale_arc", "finale_arc", "finale_arc"}
	if verifySignedBattleTicket(t, &tampered) {
		t.Fatal("deck tampering must invalidate the ticket signature")
	}
}

// The 16 -> 20 pool expansion (frost_arc / ember_lance / iron_veil /
// thunder_fall) must be deck-legal under the existing rules: the catalog now
// holds exactly deckSize ids, and a deck built from the new cards validates
// at every hop while the copy and high-rarity caps still bite.
func TestCardCatalogExpansionIsDeckLegal(t *testing.T) {
	expansion := []string{"frost_arc", "ember_lance", "iron_veil", "thunder_fall"}
	if len(serverCardCatalog) != deckSize {
		t.Fatalf("catalog must hold exactly deckSize (%d) ids, got %d", deckSize, len(serverCardCatalog))
	}
	if frost := serverCardCatalog["frost_arc"]; frost.EffectKind != "bullet_purge" || frost.Cost != 2 || frost.CooldownTicks != 360 ||
		frost.DurationTicks != 1 || frost.Damage != 0 || frost.ScoreBonus != 18 || frost.PurgeRadius != 100 || frost.PurgeCount != 12 {
		t.Fatalf("frost_arc definition drifted: %+v", frost)
	}
	if ember := serverCardCatalog["ember_lance"]; ember.EffectKind != "opponent_pattern" || ember.Cost != 3 || ember.CooldownTicks != 300 ||
		ember.DurationTicks != 240 || ember.Damage != 20 || ember.ScoreBonus != 24 || ember.PatternID != "ember_lance" {
		t.Fatalf("ember_lance definition drifted: %+v", ember)
	}
	if iron := serverCardCatalog["iron_veil"]; iron.EffectKind != "invulnerable" || iron.Cost != 4 || iron.CooldownTicks != 480 ||
		iron.DurationTicks != 120 || iron.Damage != 0 || iron.ScoreBonus != 16 {
		t.Fatalf("iron_veil definition drifted: %+v", iron)
	}
	if thunder := serverCardCatalog["thunder_fall"]; thunder.EffectKind != "finisher" || thunder.Cost != 4 || thunder.CooldownTicks != 420 ||
		thunder.DurationTicks != 1 || thunder.Damage != 30 || thunder.ScoreBonus != 40 {
		t.Fatalf("thunder_fall definition drifted: %+v", thunder)
	}
	// Only thunder_fall may count against the high-rarity cap, so two copies
	// of every new card stay legal (2 <= maxHighRareCards).
	for _, cardID := range expansion {
		if _, ok := serverCardRarities[cardID]; !ok {
			t.Fatalf("rarity table missing expansion card %q", cardID)
		}
	}
	if !isHighRareCard("thunder_fall") {
		t.Fatal("thunder_fall is epic and must count as high rarity")
	}
	for _, cardID := range []string{"frost_arc", "ember_lance", "iron_veil"} {
		if isHighRareCard(cardID) {
			t.Fatalf("%s is uncommon and must not count as high rarity", cardID)
		}
	}

	// A deck built from the four new cards (two copies each = 8 slots) topped
	// up with common/uncommon filler validates end to end and reaches the
	// signed ticket.
	filler := []string{"focus_lens", "hitbox_charm", "bomb_amplifier", "graze_engine", "draw_sigil", "purge_charm"}
	expansionDeck := make([]string, 0, deckSize)
	for _, cardID := range expansion {
		expansionDeck = append(expansionDeck, cardID, cardID)
	}
	for i := 0; len(expansionDeck) < deckSize; i++ {
		expansionDeck = append(expansionDeck, filler[i%len(filler)])
	}

	service := NewService(Config{})
	alice := mustLogin(t, service, "Expansion Alice")
	bob := mustLogin(t, service, "Expansion Bob")
	if _, err := service.SaveDeck(alice.SessionToken, SaveDeckRequest{
		DeckID:  "expansion_deck",
		Name:    "Expansion",
		Format:  defaultDeckFormat,
		CardIDs: expansionDeck,
		Active:  true,
	}); err != nil {
		t.Fatalf("deck built from the expansion cards must save: %v", err)
	}

	// Alice joins on her saved expansion deck; Bob on a stock valid deck.
	if _, err := service.JoinQueue(alice.SessionToken, JoinQueueRequest{
		ModeID:       "pvp_duel",
		ActiveDeckID: "expansion_deck",
	}); err != nil {
		t.Fatalf("alice join queue: %v", err)
	}
	if _, err := service.JoinQueue(bob.SessionToken, JoinQueueRequest{
		ModeID:       "pvp_duel",
		ActiveDeckID: fmt.Sprintf("%s_deck_0", bob.UserID),
		DeckSnapshot: validDeck(fmt.Sprintf("%s_deck_0", bob.UserID)),
	}); err != nil {
		t.Fatalf("bob join queue: %v", err)
	}
	matchID := ""
	service.mu.Lock()
	for id, match := range service.matches {
		if _, ok := match.Players[alice.UserID]; ok {
			matchID = id
			break
		}
	}
	service.mu.Unlock()
	if matchID == "" {
		t.Fatal("expansion deck match did not fill")
	}
	ticket, err := service.BattleTicket(alice.SessionToken, matchID)
	if err != nil {
		t.Fatalf("battle ticket with expansion deck: %v", err)
	}
	for _, cardID := range expansion {
		if !cardTestContains(ticket.Ticket.DeckCardIDs, cardID) {
			t.Fatalf("ticket deck missing expansion card %q: %v", cardID, ticket.Ticket.DeckCardIDs)
		}
	}

	// The two-copy limit still applies to a new card.
	overCopies := copyStringSlice(expansionDeck)
	for index, cardID := range overCopies {
		if cardID == "frost_arc" {
			overCopies[index] = "ember_lance"
			break
		}
	}
	if _, err := service.SaveDeck(alice.SessionToken, SaveDeckRequest{
		DeckID:  "over_copies",
		Name:    "Over Copies",
		Format:  defaultDeckFormat,
		CardIDs: overCopies,
	}); ErrorCode(err) != codeInvalidDeck {
		t.Fatalf("three copies of a card must be refused, got %v", err)
	}

	// The high-rarity cap still applies with the new epic in the mix:
	// 2x thunder_fall + 2x last_arc + 2x finale_arc + 2x mirror_lance = 8 > 6.
	tooManyHigh := []string{
		"thunder_fall", "thunder_fall", "last_arc", "last_arc", "finale_arc", "finale_arc", "mirror_lance", "mirror_lance",
	}
	for i := 0; len(tooManyHigh) < deckSize; i++ {
		tooManyHigh = append(tooManyHigh, filler[i%len(filler)])
	}
	if _, err := service.SaveDeck(alice.SessionToken, SaveDeckRequest{
		DeckID:  "too_many_high",
		Name:    "Too Many High",
		Format:  defaultDeckFormat,
		CardIDs: tooManyHigh,
	}); ErrorCode(err) != codeInvalidDeck {
		t.Fatalf("high-rarity cap must bite with the new epic, got %v", err)
	}
}
