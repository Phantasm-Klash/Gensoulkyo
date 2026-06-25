package cards

import (
	"errors"
	"fmt"
	"testing"
)

func TestValidateDeckAcceptsTwentyCardsWithCopyLimit(t *testing.T) {
	deck := Deck{Name: "main", Format: "open-alpha-0"}
	owned := map[string]int{}
	for i := 0; i < 10; i++ {
		code := fmt.Sprintf("card_%02d", i)
		owned[code] = 2
		deck.CardIDs = append(deck.CardIDs, code, code)
	}

	err := ValidateDeck(deck, ValidationOptions{
		DeckSize:       20,
		CopyLimit:      2,
		RulesetVersion: "open-alpha-0",
		OwnedCards:     owned,
	})
	if err != nil {
		t.Fatalf("ValidateDeck returned error: %v", err)
	}
}

func TestValidateDeckRejectsBannedCard(t *testing.T) {
	deck := Deck{Name: "ranked", Format: "open-alpha-0", CardIDs: twenty("card_a")}
	err := ValidateDeck(deck, ValidationOptions{
		DeckSize:       20,
		CopyLimit:      20,
		RulesetVersion: "open-alpha-0",
		BannedCards:    map[string]bool{"card_a": true},
	})
	if !errors.Is(err, ErrDeckBannedCard) {
		t.Fatalf("expected ErrDeckBannedCard, got %v", err)
	}
}

func TestValidateDeckRejectsTooManyCopies(t *testing.T) {
	deck := Deck{Name: "main", Format: "open-alpha-0", CardIDs: twenty("card_a")}
	err := ValidateDeck(deck, ValidationOptions{
		DeckSize:       20,
		CopyLimit:      2,
		RulesetVersion: "open-alpha-0",
	})
	if !errors.Is(err, ErrDeckTooManyCopy) {
		t.Fatalf("expected ErrDeckTooManyCopy, got %v", err)
	}
}

func TestValidateDeckRejectsMoreCopiesThanOwned(t *testing.T) {
	deck := Deck{Name: "main", Format: "open-alpha-0", CardIDs: []string{
		"card_a", "card_a",
		"card_b", "card_b",
	}}
	err := ValidateDeck(deck, ValidationOptions{
		DeckSize:       4,
		CopyLimit:      2,
		RulesetVersion: "open-alpha-0",
		OwnedCards: map[string]int{
			"card_a": 1,
			"card_b": 2,
		},
	})
	if !errors.Is(err, ErrDeckUnknownCard) {
		t.Fatalf("expected ErrDeckUnknownCard, got %v", err)
	}
}

func twenty(code string) []string {
	cards := make([]string, 20)
	for i := range cards {
		cards[i] = code
	}
	return cards
}
