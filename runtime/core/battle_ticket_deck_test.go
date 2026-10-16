package core

import (
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
