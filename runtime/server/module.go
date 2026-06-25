package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/Phantasm-Klash/Gensoulkyo/runtime/cards"
	"github.com/Phantasm-Klash/Gensoulkyo/runtime/config"
	"github.com/Phantasm-Klash/Gensoulkyo/runtime/decks"
	"github.com/Phantasm-Klash/Gensoulkyo/runtime/economy"
	gmatch "github.com/Phantasm-Klash/Gensoulkyo/runtime/match"
	"github.com/heroiclabs/nakama-common/runtime"
)

func InitModule(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, initializer runtime.Initializer) error {
	if err := initializer.RegisterRpc("gensoulkyo.config.get", rpcConfigGet); err != nil {
		return err
	}
	if err := initializer.RegisterRpc("gensoulkyo.profile.get", rpcProfileGet); err != nil {
		return err
	}
	if err := initializer.RegisterRpc("gensoulkyo.deck.save", rpcDeckSave); err != nil {
		return err
	}
	if err := initializer.RegisterRpc("gensoulkyo.deck.list", rpcDeckList); err != nil {
		return err
	}
	if err := initializer.RegisterRpc("gensoulkyo.inventory.get", rpcInventoryGet); err != nil {
		return err
	}
	if err := initializer.RegisterRpc("gensoulkyo.chest.pool.list", rpcChestPoolList); err != nil {
		return err
	}
	if err := initializer.RegisterRpc("gensoulkyo.chest.open", rpcChestOpen); err != nil {
		return err
	}
	if err := initializer.RegisterRpc("gensoulkyo.room.create", rpcRoomCreate); err != nil {
		return err
	}
	if err := initializer.RegisterRpc("gensoulkyo.room.join", rpcRoomJoin); err != nil {
		return err
	}
	if err := initializer.RegisterRpc("gensoulkyo.room.start", rpcRoomStart); err != nil {
		return err
	}
	if err := initializer.RegisterRpc("gensoulkyo.match.create", rpcMatchCreate); err != nil {
		return err
	}
	if err := initializer.RegisterMatch("gensoulkyo.duel", func(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule) (runtime.Match, error) {
		return &gmatch.Match{}, nil
	}); err != nil {
		return err
	}
	logger.Info("Gensoulkyo Nakama runtime initialized")
	return nil
}

func rpcConfigGet(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	return marshal(config.Current())
}

func rpcProfileGet(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	userID, err := authenticatedUserID(ctx)
	if err != nil {
		return "", err
	}
	wallet, err := getOrCreateWallet(ctx, db, userID)
	if err != nil {
		return "", err
	}
	response := map[string]interface{}{
		"user_id":         userID,
		"wallet":          wallet,
		"ruleset_version": config.CurrentRulesetVersion,
	}
	return marshal(response)
}

func rpcDeckSave(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	userID, err := authenticatedUserID(ctx)
	if err != nil {
		return "", err
	}
	var request struct {
		ID      string   `json:"id"`
		Name    string   `json:"name"`
		Format  string   `json:"format"`
		CardIDs []string `json:"card_ids"`
		Active  bool     `json:"active"`
	}
	if err := json.Unmarshal([]byte(payload), &request); err != nil {
		return "", err
	}
	deck := cards.Deck{
		ID:      request.ID,
		Name:    request.Name,
		Format:  request.Format,
		CardIDs: request.CardIDs,
		Active:  request.Active,
	}
	snapshot, err := decks.Save(ctx, db, userID, deck)
	if err != nil {
		return "", err
	}
	return marshal(map[string]interface{}{
		"id":              snapshot.DeckID,
		"name":            snapshot.Name,
		"format":          snapshot.Format,
		"card_ids":        snapshot.CardIDs,
		"active":          request.Active,
		"deck_snapshot":   snapshot,
		"ruleset_version": snapshot.RulesetVersion,
	})
}

func rpcDeckList(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	userID, err := authenticatedUserID(ctx)
	if err != nil {
		return "", err
	}
	playerDecks, err := decks.List(ctx, db, userID)
	if err != nil {
		return "", err
	}
	return marshal(map[string]interface{}{"decks": playerDecks})
}

func rpcInventoryGet(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	userID, err := authenticatedUserID(ctx)
	if err != nil {
		return "", err
	}
	wallet, err := getOrCreateWallet(ctx, db, userID)
	if err != nil {
		return "", err
	}
	inventory, err := listInventory(ctx, db, userID)
	if err != nil {
		return "", err
	}
	return marshal(map[string]interface{}{
		"wallet":    wallet,
		"inventory": inventory,
	})
}

func rpcChestPoolList(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	rows, err := db.QueryContext(ctx, `
		select id::text, season_id, name, cost_json, weights_json, pity_json, starts_at::text, ends_at::text
		from chest_pools
		where enabled = true
		  and starts_at <= now()
		  and (ends_at is null or ends_at > now())
		order by starts_at desc, name asc
	`)
	if err != nil {
		return "", err
	}
	defer rows.Close()

	pools := make([]map[string]interface{}, 0)
	for rows.Next() {
		var pool chestPool
		if err := rows.Scan(&pool.ID, &pool.SeasonID, &pool.Name, &pool.CostJSON, &pool.WeightsJSON, &pool.PityJSON, &pool.StartsAt, &pool.EndsAt); err != nil {
			return "", err
		}
		pools = append(pools, pool.response())
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	return marshal(map[string]interface{}{"pools": pools})
}

func rpcChestOpen(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	userID, err := authenticatedUserID(ctx)
	if err != nil {
		return "", err
	}
	var request struct {
		PoolID string `json:"pool_id"`
	}
	if err := json.Unmarshal([]byte(payload), &request); err != nil {
		return "", err
	}
	if strings.TrimSpace(request.PoolID) == "" {
		return "", errors.New("pool_id is required")
	}
	result, err := openChest(ctx, db, userID, request.PoolID)
	if err != nil {
		return "", err
	}
	return marshal(result)
}

func rpcMatchCreate(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	userID, err := authenticatedUserID(ctx)
	if err != nil {
		return "", err
	}
	var request struct {
		Mode       string   `json:"mode"`
		OpponentID string   `json:"opponent_id"`
		UserIDs    []string `json:"user_ids"`
		EndTick    int      `json:"end_tick"`
	}
	if err := json.Unmarshal([]byte(payload), &request); err != nil && strings.TrimSpace(payload) != "" {
		return "", err
	}
	mode := strings.TrimSpace(request.Mode)
	if mode == "" {
		mode = "duel"
	}
	if mode != "duel" {
		return "", fmt.Errorf("unsupported match mode: %s", mode)
	}

	userIDs := append([]string{userID}, request.UserIDs...)
	if strings.TrimSpace(request.OpponentID) != "" {
		userIDs = append(userIDs, strings.TrimSpace(request.OpponentID))
	}
	userIDs = uniqueNonEmpty(userIDs)
	if len(userIDs) > 2 {
		return "", errors.New("duel supports at most two players")
	}

	deckSnapshots := make(map[string]cards.DeckSnapshot, len(userIDs))
	deckSnapshotPayload := make(map[string]interface{}, len(userIDs))
	for _, participantID := range userIDs {
		snapshot, err := decks.ActiveSnapshot(ctx, db, participantID)
		if err != nil {
			return "", fmt.Errorf("active deck validation failed for %s: %w", participantID, err)
		}
		deckSnapshots[participantID] = snapshot
		deckSnapshotPayload[participantID] = snapshot
	}

	params := map[string]interface{}{
		"mode":           mode,
		"match_id":       newUUID(),
		"user_ids":       stringsForParams(userIDs),
		"deck_snapshots": deckSnapshotPayload,
	}
	if request.EndTick > 0 {
		params["end_tick"] = request.EndTick
	}
	matchID, err := nk.MatchCreate(ctx, "gensoulkyo.duel", params)
	if err != nil {
		return "", err
	}
	return marshal(map[string]interface{}{
		"match_id":        params["match_id"],
		"nakama_match_id": matchID,
		"mode":            mode,
		"ruleset_version": config.CurrentRulesetVersion,
		"user_ids":        userIDs,
		"deck_snapshots":  deckSnapshots,
	})
}

func marshal(value interface{}) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func uniqueNonEmpty(values []string) []string {
	seen := make(map[string]bool, len(values))
	result := make([]string, 0, len(values))
	for _, raw := range values {
		value := strings.TrimSpace(raw)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	return result
}

func stringsForParams(values []string) []interface{} {
	result := make([]interface{}, 0, len(values))
	for _, value := range values {
		result = append(result, value)
	}
	return result
}

func authenticatedUserID(ctx context.Context) (string, error) {
	userID, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if !ok || userID == "" {
		return "", errors.New("missing authenticated user")
	}
	return userID, nil
}

type wallet struct {
	Points    int64 `json:"points"`
	CardDust  int64 `json:"card_dust"`
	ChestKeys int64 `json:"chest_keys"`
}

type inventoryItem struct {
	CardID          string `json:"card_id"`
	Code            string `json:"code"`
	Rarity          string `json:"rarity"`
	Copies          int    `json:"copies"`
	Level           int    `json:"level"`
	FirstObtainedAt string `json:"first_obtained_at"`
}

type chestCost struct {
	Points    int64 `json:"points"`
	CardDust  int64 `json:"card_dust"`
	ChestKeys int64 `json:"chest_keys"`
}

type chestPool struct {
	ID          string
	SeasonID    string
	Name        string
	CostJSON    []byte
	WeightsJSON []byte
	PityJSON    []byte
	StartsAt    string
	EndsAt      sql.NullString
}

func (p chestPool) response() map[string]interface{} {
	return map[string]interface{}{
		"id":           p.ID,
		"season_id":    p.SeasonID,
		"name":         p.Name,
		"cost":         jsonObject(p.CostJSON),
		"weights":      jsonObject(p.WeightsJSON),
		"pity":         jsonObject(p.PityJSON),
		"starts_at":    p.StartsAt,
		"ends_at":      nullableString(p.EndsAt),
		"ruleset_hint": config.CurrentRulesetVersion,
	}
}

func getOrCreateWallet(ctx context.Context, db *sql.DB, userID string) (wallet, error) {
	var w wallet
	err := db.QueryRowContext(ctx, `
		insert into player_wallets (user_id, updated_at)
		values ($1, now())
		on conflict (user_id) do update set updated_at = player_wallets.updated_at
		returning points, card_dust, chest_keys
	`, userID).Scan(&w.Points, &w.CardDust, &w.ChestKeys)
	return w, err
}

func listInventory(ctx context.Context, db *sql.DB, userID string) ([]inventoryItem, error) {
	rows, err := db.QueryContext(ctx, `
		select c.id::text, c.code, c.rarity, i.copies, i.level, i.first_obtained_at::text
		from player_card_inventory i
		join cards c on c.id = i.card_id
		where i.user_id = $1
		order by c.code asc
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]inventoryItem, 0)
	for rows.Next() {
		var item inventoryItem
		if err := rows.Scan(&item.CardID, &item.Code, &item.Rarity, &item.Copies, &item.Level, &item.FirstObtainedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func openChest(ctx context.Context, db *sql.DB, userID, poolID string) (map[string]interface{}, error) {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	pool, cost, rewards, err := loadChestPool(ctx, tx, poolID)
	if err != nil {
		return nil, err
	}
	if err := chargeWallet(ctx, tx, userID, cost); err != nil {
		return nil, err
	}
	openingIndex, err := nextOpeningIndex(ctx, tx, userID, poolID)
	if err != nil {
		return nil, err
	}
	result, err := economy.OpenChest(userID, poolID, openingIndex, rewards)
	if err != nil {
		return nil, err
	}
	if err := grantCard(ctx, tx, userID, result.RewardCode); err != nil {
		return nil, err
	}
	resultJSON, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	costJSON, err := json.Marshal(cost)
	if err != nil {
		return nil, err
	}
	var openingID string
	if err := tx.QueryRowContext(ctx, `
		insert into chest_openings (user_id, pool_id, server_seed, result_json, cost_json, created_at)
		values ($1, $2, $3, $4::jsonb, $5::jsonb, now())
		returning id::text
	`, userID, poolID, result.ServerSeed, resultJSON, costJSON).Scan(&openingID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return map[string]interface{}{
		"opening_id": openingID,
		"pool":       pool.response(),
		"result":     result,
	}, nil
}

func loadChestPool(ctx context.Context, tx *sql.Tx, poolID string) (chestPool, chestCost, []economy.RewardWeight, error) {
	var pool chestPool
	err := tx.QueryRowContext(ctx, `
		select id::text, season_id, name, cost_json, weights_json, pity_json, starts_at::text, ends_at::text
		from chest_pools
		where id = $1
		  and enabled = true
		  and starts_at <= now()
		  and (ends_at is null or ends_at > now())
		for update
	`, poolID).Scan(&pool.ID, &pool.SeasonID, &pool.Name, &pool.CostJSON, &pool.WeightsJSON, &pool.PityJSON, &pool.StartsAt, &pool.EndsAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return chestPool{}, chestCost{}, nil, errors.New("chest pool is unavailable")
		}
		return chestPool{}, chestCost{}, nil, err
	}
	var cost chestCost
	if err := json.Unmarshal(pool.CostJSON, &cost); err != nil {
		return chestPool{}, chestCost{}, nil, fmt.Errorf("invalid chest cost config: %w", err)
	}
	if cost.Points < 0 || cost.CardDust < 0 || cost.ChestKeys < 0 {
		return chestPool{}, chestCost{}, nil, errors.New("invalid negative chest cost config")
	}
	var rewards []economy.RewardWeight
	if err := json.Unmarshal(pool.WeightsJSON, &rewards); err != nil {
		return chestPool{}, chestCost{}, nil, fmt.Errorf("invalid chest reward config: %w", err)
	}
	return pool, cost, rewards, nil
}

func chargeWallet(ctx context.Context, tx *sql.Tx, userID string, cost chestCost) error {
	var w wallet
	err := tx.QueryRowContext(ctx, `
		insert into player_wallets (user_id, updated_at)
		values ($1, now())
		on conflict (user_id) do update set updated_at = player_wallets.updated_at
		returning points, card_dust, chest_keys
	`, userID).Scan(&w.Points, &w.CardDust, &w.ChestKeys)
	if err != nil {
		return err
	}
	if w.Points < cost.Points || w.CardDust < cost.CardDust || w.ChestKeys < cost.ChestKeys {
		return errors.New("insufficient wallet balance")
	}
	_, err = tx.ExecContext(ctx, `
		update player_wallets
		set points = points - $2,
		    card_dust = card_dust - $3,
		    chest_keys = chest_keys - $4,
		    updated_at = now()
		where user_id = $1
	`, userID, cost.Points, cost.CardDust, cost.ChestKeys)
	return err
}

func nextOpeningIndex(ctx context.Context, tx *sql.Tx, userID, poolID string) (int64, error) {
	var count int64
	if err := tx.QueryRowContext(ctx, `
		select count(*)
		from chest_openings
		where user_id = $1 and pool_id = $2
	`, userID, poolID).Scan(&count); err != nil {
		return 0, err
	}
	return count + 1, nil
}

func grantCard(ctx context.Context, tx *sql.Tx, userID, cardCode string) error {
	var cardID string
	if err := tx.QueryRowContext(ctx, `
		select id::text
		from cards
		where code = $1 and enabled = true
	`, cardCode).Scan(&cardID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("reward card is unavailable: %s", cardCode)
		}
		return err
	}
	_, err := tx.ExecContext(ctx, `
		insert into player_card_inventory (user_id, card_id, copies, level, first_obtained_at)
		values ($1, $2, 1, 1, now())
		on conflict (user_id, card_id) do update
		set copies = player_card_inventory.copies + 1
	`, userID, cardID)
	return err
}

func jsonObject(raw []byte) interface{} {
	var value interface{}
	if err := json.Unmarshal(raw, &value); err != nil {
		return map[string]interface{}{}
	}
	return value
}

func nullableString(value sql.NullString) interface{} {
	if !value.Valid {
		return nil
	}
	return value.String
}
