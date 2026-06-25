package cards

import (
	"errors"
	"fmt"
	"strings"
)

var (
	ErrDeckWrongSize    = errors.New("deck must contain exactly 20 cards")
	ErrDeckEmptyCard    = errors.New("deck contains an empty card code")
	ErrDeckTooManyCopy  = errors.New("deck contains too many copies of a card")
	ErrDeckBannedCard   = errors.New("deck contains a banned card")
	ErrDeckUnknownCard  = errors.New("deck contains a card the player does not own")
	ErrDeckBadRuleset   = errors.New("deck format is not compatible with the current ruleset")
	ErrDeckNameRequired = errors.New("deck name is required")
)

type Definition struct {
	Code    string `json:"code"`
	Enabled bool   `json:"enabled"`
}

type Deck struct {
	ID      string   `json:"id,omitempty"`
	Name    string   `json:"name"`
	Format  string   `json:"format"`
	CardIDs []string `json:"card_ids"`
	Active  bool     `json:"active"`
}

type ValidationOptions struct {
	DeckSize       int
	CopyLimit      int
	RulesetVersion string
	OwnedCards     map[string]int
	BannedCards    map[string]bool
}

func ValidateDeck(deck Deck, opts ValidationOptions) error {
	if strings.TrimSpace(deck.Name) == "" {
		return ErrDeckNameRequired
	}
	if deck.Format != "" && deck.Format != opts.RulesetVersion {
		return fmt.Errorf("%w: %s", ErrDeckBadRuleset, deck.Format)
	}
	if len(deck.CardIDs) != opts.DeckSize {
		return fmt.Errorf("%w: got %d want %d", ErrDeckWrongSize, len(deck.CardIDs), opts.DeckSize)
	}

	counts := make(map[string]int, len(deck.CardIDs))
	for _, raw := range deck.CardIDs {
		code := strings.TrimSpace(raw)
		if code == "" {
			return ErrDeckEmptyCard
		}
		if opts.BannedCards != nil && opts.BannedCards[code] {
			return fmt.Errorf("%w: %s", ErrDeckBannedCard, code)
		}
		if opts.OwnedCards != nil && opts.OwnedCards[code] <= 0 {
			return fmt.Errorf("%w: %s", ErrDeckUnknownCard, code)
		}
		counts[code]++
		if opts.OwnedCards != nil && opts.OwnedCards[code] > 0 && counts[code] > opts.OwnedCards[code] {
			return fmt.Errorf("%w: %s", ErrDeckUnknownCard, code)
		}
		if opts.CopyLimit > 0 && counts[code] > opts.CopyLimit {
			return fmt.Errorf("%w: %s", ErrDeckTooManyCopy, code)
		}
	}

	return nil
}

func Snapshot(deck Deck, rulesetVersion string) DeckSnapshot {
	cards := append([]string(nil), deck.CardIDs...)
	return DeckSnapshot{
		DeckID:         deck.ID,
		Name:           deck.Name,
		Format:         deck.Format,
		RulesetVersion: rulesetVersion,
		CardIDs:        cards,
	}
}

type DeckSnapshot struct {
	DeckID         string   `json:"deck_id"`
	Name           string   `json:"name"`
	Format         string   `json:"format"`
	RulesetVersion string   `json:"ruleset_version"`
	CardIDs        []string `json:"card_ids"`
}
