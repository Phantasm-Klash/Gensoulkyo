package decks

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/Phantasm-Klash/Gensoulkyo/runtime/cards"
	"github.com/Phantasm-Klash/Gensoulkyo/runtime/config"
)

var (
	ErrDeckUnavailable = errors.New("deck is unavailable")
	ErrNoActiveDeck    = errors.New("active deck is required")
)

func Save(ctx context.Context, db *sql.DB, userID string, deck cards.Deck) (cards.DeckSnapshot, error) {
	if db == nil {
		return cards.DeckSnapshot{}, errors.New("database is unavailable")
	}
	deck.Name = strings.TrimSpace(deck.Name)
	if deck.Format == "" {
		deck.Format = config.CurrentRulesetVersion
	}
	for i, code := range deck.CardIDs {
		deck.CardIDs[i] = strings.TrimSpace(code)
	}
	ownedCards, err := OwnedCards(ctx, db, userID)
	if err != nil {
		return cards.DeckSnapshot{}, err
	}
	if err := cards.ValidateDeck(deck, cards.ValidationOptions{
		DeckSize:       config.DefaultDeckSize,
		CopyLimit:      config.DefaultDeckCopyLimit,
		RulesetVersion: config.CurrentRulesetVersion,
		OwnedCards:     ownedCards,
	}); err != nil {
		return cards.DeckSnapshot{}, err
	}
	cardIDs, err := json.Marshal(deck.CardIDs)
	if err != nil {
		return cards.DeckSnapshot{}, err
	}

	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return cards.DeckSnapshot{}, err
	}
	defer tx.Rollback()

	if deck.Active {
		if _, err := tx.ExecContext(ctx, `
			update player_decks
			set active = false,
			    updated_at = now()
			where user_id = $1 and active = true
		`, userID); err != nil {
			return cards.DeckSnapshot{}, err
		}
	}

	if deck.ID == "" {
		err = tx.QueryRowContext(ctx, `
			insert into player_decks (user_id, name, format, card_ids, active, updated_at)
			values ($1, $2, $3, $4::jsonb, $5, now())
			returning id::text
		`, userID, deck.Name, deck.Format, cardIDs, deck.Active).Scan(&deck.ID)
	} else {
		err = tx.QueryRowContext(ctx, `
			insert into player_decks (id, user_id, name, format, card_ids, active, updated_at)
			values ($1, $2, $3, $4, $5::jsonb, $6, now())
			on conflict (id) do update
			set name = excluded.name,
			    format = excluded.format,
			    card_ids = excluded.card_ids,
			    active = excluded.active,
			    updated_at = now()
			where player_decks.user_id = excluded.user_id
			returning id::text
		`, deck.ID, userID, deck.Name, deck.Format, cardIDs, deck.Active).Scan(&deck.ID)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return cards.DeckSnapshot{}, ErrDeckUnavailable
	}
	if err != nil {
		return cards.DeckSnapshot{}, err
	}
	if err := tx.Commit(); err != nil {
		return cards.DeckSnapshot{}, err
	}
	return cards.Snapshot(deck, config.CurrentRulesetVersion), nil
}

func List(ctx context.Context, db *sql.DB, userID string) ([]cards.Deck, error) {
	rows, err := db.QueryContext(ctx, `
		select id::text, name, format, card_ids, active
		from player_decks
		where user_id = $1
		order by active desc, updated_at desc, name asc
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make([]cards.Deck, 0)
	for rows.Next() {
		var deck cards.Deck
		var cardIDs []byte
		if err := rows.Scan(&deck.ID, &deck.Name, &deck.Format, &cardIDs, &deck.Active); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(cardIDs, &deck.CardIDs); err != nil {
			return nil, fmt.Errorf("invalid stored deck cards for %s: %w", deck.ID, err)
		}
		result = append(result, deck)
	}
	return result, rows.Err()
}

func ActiveSnapshot(ctx context.Context, db *sql.DB, userID string) (cards.DeckSnapshot, error) {
	deck, err := loadActive(ctx, db, userID)
	if err != nil {
		return cards.DeckSnapshot{}, err
	}
	ownedCards, err := OwnedCards(ctx, db, userID)
	if err != nil {
		return cards.DeckSnapshot{}, err
	}
	if err := cards.ValidateDeck(deck, cards.ValidationOptions{
		DeckSize:       config.DefaultDeckSize,
		CopyLimit:      config.DefaultDeckCopyLimit,
		RulesetVersion: config.CurrentRulesetVersion,
		OwnedCards:     ownedCards,
	}); err != nil {
		return cards.DeckSnapshot{}, err
	}
	return cards.Snapshot(deck, config.CurrentRulesetVersion), nil
}

func OwnedCards(ctx context.Context, db *sql.DB, userID string) (map[string]int, error) {
	rows, err := db.QueryContext(ctx, `
		select c.code, i.copies
		from player_card_inventory i
		join cards c on c.id = i.card_id
		where i.user_id = $1
		  and c.enabled = true
		  and i.copies > 0
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	owned := make(map[string]int)
	for rows.Next() {
		var code string
		var copies int
		if err := rows.Scan(&code, &copies); err != nil {
			return nil, err
		}
		owned[code] = copies
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(owned) == 0 {
		return cards.DefaultOwnedCards(), nil
	}
	return owned, nil
}

func loadActive(ctx context.Context, db *sql.DB, userID string) (cards.Deck, error) {
	var deck cards.Deck
	var cardIDs []byte
	err := db.QueryRowContext(ctx, `
		select id::text, name, format, card_ids, active
		from player_decks
		where user_id = $1 and active = true
		order by updated_at desc
		limit 1
	`, userID).Scan(&deck.ID, &deck.Name, &deck.Format, &cardIDs, &deck.Active)
	if errors.Is(err, sql.ErrNoRows) {
		return cards.Deck{}, ErrNoActiveDeck
	}
	if err != nil {
		return cards.Deck{}, err
	}
	if err := json.Unmarshal(cardIDs, &deck.CardIDs); err != nil {
		return cards.Deck{}, fmt.Errorf("invalid active deck cards for %s: %w", deck.ID, err)
	}
	return deck, nil
}
